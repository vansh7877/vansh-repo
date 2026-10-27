// Command gen produces realistic synthetic CSP transactions with every fraud
// pattern injected, either as a CSV file or streamed to the webhook.
//
//	go run ./cmd/gen -out sample.csv
//	go run ./cmd/gen -post http://localhost:8080/api/v1/transactions -rate 200
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

type txn struct {
	ID, CSP, KO, From, To, Type, Channel, Status, Peer string
	Time                                               time.Time
	Amount, Charge                                     float64
}

type gen struct {
	rnd   *rand.Rand
	txns  []txn
	seq   int
	csps  []string
	kos   map[string]string
	peers map[string]string
	accts map[string][]string
	payee map[string][]string
	start time.Time
	days  int
}

func main() {
	out := flag.String("out", "", "write CSV to this file (default stdout)")
	post := flag.String("post", "", "stream transactions to this webhook URL instead of writing CSV")
	rate := flag.Int("rate", 200, "transactions per second when posting")
	batch := flag.Int("batch", 1, "transactions per webhook request when posting")
	ncsp := flag.Int("csps", 80, "number of CSPs")
	days := flag.Int("days", 60, "number of days of history")
	seed := flag.Int64("seed", 42, "random seed")
	end := flag.String("end", time.Now().Format("2006-01-02"), "last day (YYYY-MM-DD)")
	flag.Parse()

	ist, _ := time.LoadLocation("Asia/Kolkata")
	if ist == nil {
		ist = time.FixedZone("IST", 19800)
	}
	last, err := time.ParseInLocation("2006-01-02", *end, ist)
	if err != nil {
		log.Fatal(err)
	}
	g := &gen{rnd: rand.New(rand.NewSource(*seed)), kos: map[string]string{}, peers: map[string]string{},
		accts: map[string][]string{}, payee: map[string][]string{}, start: last.AddDate(0, 0, -*days+1), days: *days}
	g.setup(*ncsp)
	g.normal()
	g.inject()
	sort.SliceStable(g.txns, func(i, j int) bool { return g.txns[i].Time.Before(g.txns[j].Time) })

	if *post != "" {
		g.stream(*post, *rate, *batch)
		return
	}
	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		w = f
	}
	g.writeCSV(w)
	log.Printf("generated %d transactions for %d CSPs over %d days", len(g.txns), *ncsp, *days)
}

func (g *gen) setup(n int) {
	regions := []string{"North", "South", "East", "West"}
	for i := 1; i <= n; i++ {
		c := fmt.Sprintf("CSP%04d", i)
		g.csps = append(g.csps, c)
		g.kos[c] = fmt.Sprintf("KO%06d", 100000+i)
		g.peers[c] = regions[i%len(regions)]
		for k := 0; k < 800; k++ {
			g.accts[c] = append(g.accts[c], g.account())
		}
	}
}

func (g *gen) account() string { return fmt.Sprintf("%012d", 300000000000+g.rnd.Int63n(99999999999)) }

func (g *gen) csp(i int) string { return g.csps[(i-1)%len(g.csps)] }

func (g *gen) add(t txn) {
	g.seq++
	if t.ID == "" {
		t.ID = fmt.Sprintf("TXN%08d", g.seq)
	}
	if t.KO == "" {
		t.KO = g.kos[t.CSP]
	}
	if t.Channel == "" {
		t.Channel = "ONUS"
	}
	if t.Status == "" {
		t.Status = "SUCCESS"
	}
	t.Peer = g.peers[t.CSP]
	g.txns = append(g.txns, t)
}

func (g *gen) day(d int) time.Time { return g.start.AddDate(0, 0, d) }

// at returns a time on day d during business hours.
func (g *gen) at(d int) time.Time {
	return g.day(d).Add(time.Duration(9*3600+g.rnd.Intn(10*3600)) * time.Second)
}

// amount draws a log-normal amount; people mostly transact round figures.
func (g *gen) amount() float64 {
	a := math.Exp(7.3 + g.rnd.NormFloat64()*0.9) // median ≈ ₹1,500
	a = math.Min(math.Max(a, 100), 45000)
	switch r := g.rnd.Float64(); {
	case r < 0.6:
		return math.Max(100, math.Round(a/100)*100)
	case r < 0.85:
		return math.Max(500, math.Round(a/500)*500)
	default:
		return math.Round(a)
	}
}

// payeeOf returns one of a customer's two regular transfer beneficiaries.
func (g *gen) payeeOf(acct string) string {
	p := g.payee[acct]
	if p == nil {
		for k := 0; k < 2; k++ {
			other := g.csps[g.rnd.Intn(len(g.csps))]
			p = append(p, g.cust(other))
		}
		g.payee[acct] = p
	}
	return p[g.rnd.Intn(len(p))]
}

func (g *gen) cust(c string) string { a := g.accts[c]; return a[g.rnd.Intn(len(a))] }

func (g *gen) normalTxn(c string, t time.Time) {
	x := txn{CSP: c, Time: t, Amount: g.amount()}
	switch p := g.rnd.Float64(); {
	case p < 0.42:
		x.Type, x.To = "Cash Deposit", g.cust(c)
	case p < 0.82:
		x.Type, x.From = "Cash Withdrawal", g.cust(c)
	case p < 0.96:
		x.Type, x.From = "Money Transfer", g.cust(c)
		x.To = g.payeeOf(x.From)
		x.Charge = 10
		if g.rnd.Float64() < 0.3 {
			x.Charge = 0
		}
	case p < 0.98:
		x.Type, x.To = "Loan Disbursement", g.cust(c)
	default:
		x.Type, x.To = "Initial Deposit", g.account()
		x.Amount = 500
	}
	// Avoid accidental exact-amount hits in the background noise.
	for x.Amount == 436 || x.Amount == 50 {
		x.Amount += 10
	}
	if g.rnd.Float64() < 0.12 {
		x.Channel = "OFFUS"
		if x.From != "" {
			x.From = "XXXXXX" + x.From[8:]
		}
	}
	if g.rnd.Float64() < 0.02 {
		x.Status = "FAILED"
	}
	g.add(x)
}

func (g *gen) normal() {
	for i, c := range g.csps {
		baseRate := 25 + g.rnd.Intn(25)
		for d := 0; d < g.days; d++ {
			if g.rnd.Float64() < 0.08 { // occasional day off
				continue
			}
			n := baseRate + g.rnd.Intn(10) - 5
			switch i + 1 {
			case 31: // low attendance: works ~1 day in 4
				if d%4 != 0 {
					continue
				}
			case 37: // unusually high volume
				n *= 3
			case 41: // volume jumps in the final 3 weeks
				if d >= g.days-21 {
					n *= 2
				}
			}
			for k := 0; k < n; k++ {
				t := g.at(d)
				if i+1 == 43 && g.rnd.Float64() < 0.4 { // night operator
					t = g.day(d).Add(time.Duration(22*3600+g.rnd.Intn(7*3600)) * time.Second)
				}
				g.normalTxn(c, t)
			}
		}
	}
}

func (g *gen) inject() {
	recent := func() int { return g.days - 1 - g.rnd.Intn(min(g.days, 20)) }

	// 1. Split: one ₹36,000 deposit split into 4 × ₹9,000.
	for k := 0; k < 25; k++ {
		c, d := g.csp(3), recent()
		acct := g.cust(c)
		t := g.at(d)
		for j := 0; j < 4; j++ {
			g.add(txn{CSP: c, Type: "Cash Deposit", To: acct, Amount: 9000, Time: t.Add(time.Duration(j*4) * time.Minute)})
		}
	}
	// 2. Round trip: A → B → C → A on the same day.
	for k := 0; k < 15; k++ {
		c, d := g.csp(7), recent()
		a, b, cc := g.cust(c), g.cust(c), g.cust(c)
		t := g.at(d)
		amt := float64(5000 + 500*g.rnd.Intn(10))
		g.add(txn{CSP: c, Type: "Money Transfer", From: a, To: b, Amount: amt, Charge: 10, Time: t})
		g.add(txn{CSP: c, Type: "Money Transfer", From: b, To: cc, Amount: amt - 100, Charge: 10, Time: t.Add(40 * time.Minute)})
		g.add(txn{CSP: c, Type: "Money Transfer", From: cc, To: a, Amount: amt - 200, Charge: 10, Time: t.Add(80 * time.Minute)})
	}
	// 3. 1% extra charges.
	for k := 0; k < 30; k++ {
		c, d := g.csp(11), recent()
		if k%3 == 0 {
			g.add(txn{CSP: c, Type: "Cash Deposit", To: g.cust(c), Amount: []float64{4950, 9900, 1980}[k%3], Time: g.at(d)})
		} else {
			g.add(txn{CSP: c, Type: "Cash Withdrawal", From: g.cust(c), Amount: []float64{5050, 2020, 10100}[k%3], Time: g.at(d)})
		}
	}
	// 4. ₹1 test deposit, then cash-out.
	for k := 0; k < 6; k++ {
		c, d := g.csp(13), recent()
		acct := g.account()
		t := g.at(d)
		g.add(txn{CSP: c, Type: "Cash Deposit", To: acct, Amount: 1, Time: t})
		g.add(txn{CSP: c, Type: "Cash Withdrawal", From: acct, Amount: float64(8000 + 500*k), Time: t.Add(2 * time.Hour)})
	}
	// 5. Transfers from many customers into the CSP's relative's account.
	rel := g.account()
	for k := 0; k < 12; k++ {
		c := g.csp(17)
		g.add(txn{CSP: c, Type: "Money Transfer", From: g.cust(c), To: rel, Amount: float64(1500 + 250*k), Charge: 10, Time: g.at(recent())})
	}
	// 6. ₹436 debits.
	for k := 0; k < 10; k++ {
		c := g.csp(19)
		g.add(txn{CSP: c, Type: "Cash Withdrawal", From: g.cust(c), Amount: 436, Time: g.at(recent())})
	}
	// 7. Heavy sub-KO usage: re-tag most of CSP23's transactions.
	sub := g.kos[g.csp(23)] + "01"
	for i := range g.txns {
		if g.txns[i].CSP == g.csp(23) && g.rnd.Float64() < 0.7 {
			g.txns[i].KO = sub
		}
	}
	// 8. Many ₹50 transactions.
	for k := 0; k < 60; k++ {
		c := g.csp(29)
		g.add(txn{CSP: c, Type: "Cash Withdrawal", From: g.cust(c), Amount: 50, Time: g.at(recent())})
	}
	// 14. Cross-CSP money cycle and a hub account.
	ring := []string{g.account(), g.account(), g.account(), g.account()}
	ringCSPs := []string{g.csp(47), g.csp(53), g.csp(59)}
	for k := 0; k < 4; k++ {
		d := recent()
		for j := range ring {
			g.add(txn{CSP: ringCSPs[(j+k)%3], Type: "Money Transfer", From: ring[j], To: ring[(j+1)%len(ring)],
				Amount: float64(20000 - 1000*j), Charge: 10, Time: g.at(d)})
		}
	}
	hub := g.account()
	for k := 0; k < 8; k++ {
		c := g.csp(5 + 9*k)
		g.add(txn{CSP: c, Type: "Money Transfer", From: g.cust(c), To: hub, Amount: float64(3000 + 700*k), Charge: 10, Time: g.at(recent())})
	}
	// Mule: credit and immediate cash-out.
	for k := 0; k < 5; k++ {
		c := g.csp(61)
		mule := g.cust(c)
		t := g.at(recent())
		g.add(txn{CSP: c, Type: "Money Transfer", From: g.cust(g.csp(2)), To: mule, Amount: 25000, Charge: 10, Time: t})
		g.add(txn{CSP: c, Type: "Cash Withdrawal", From: mule, Amount: 24000, Time: t.Add(12 * time.Minute)})
	}
	// Duplicate submissions.
	for k := 0; k < 8; k++ {
		c := g.csp(67)
		t := g.at(recent())
		x := txn{CSP: c, Type: "Cash Withdrawal", From: g.cust(c), Amount: float64(2000 + 100*k), Time: t}
		g.add(x)
		x.ID, x.Time = "", t.Add(50*time.Second)
		g.add(x)
	}
	// Failed attempts then success.
	for k := 0; k < 4; k++ {
		c := g.csp(71)
		acct := g.cust(c)
		t := g.at(recent())
		for j := 0; j < 4; j++ {
			g.add(txn{CSP: c, Type: "Cash Withdrawal", From: acct, Amount: 9000, Status: "FAILED", Time: t.Add(time.Duration(j) * time.Minute)})
		}
		g.add(txn{CSP: c, Type: "Cash Withdrawal", From: acct, Amount: 9000, Time: t.Add(5 * time.Minute)})
	}
	// Threshold avoidance: twice ₹49,500 on the same day.
	for k := 0; k < 4; k++ {
		c := g.csp(73)
		acct := g.cust(c)
		t := g.at(recent())
		g.add(txn{CSP: c, Type: "Cash Deposit", To: acct, Amount: 49500, Time: t})
		g.add(txn{CSP: c, Type: "Cash Deposit", To: acct, Amount: 49000, Time: t.Add(3 * time.Hour)})
	}
}

var header = []string{"txn_id", "timestamp", "csp_id", "ko_id", "from_account", "to_account", "txn_type",
	"amount", "customer_charge", "channel", "status", "peer_group"}

func (t txn) row() []string {
	return []string{t.ID, t.Time.Format("2006-01-02 15:04:05"), t.CSP, t.KO, t.From, t.To, t.Type,
		strconv.FormatFloat(t.Amount, 'f', 2, 64), strconv.FormatFloat(t.Charge, 'f', 2, 64), t.Channel, t.Status, t.Peer}
}

func (g *gen) writeCSV(w io.Writer) {
	cw := csv.NewWriter(w)
	cw.Write(header)
	for _, t := range g.txns {
		cw.Write(t.row())
	}
	cw.Flush()
}

func (g *gen) stream(url string, rate, batch int) {
	if batch < 1 {
		batch = 1
	}
	interval := time.Second * time.Duration(batch) / time.Duration(max(rate, 1))
	tick := time.NewTicker(interval)
	defer tick.Stop()
	client := &http.Client{Timeout: 10 * time.Second}
	sent, flagged := 0, 0
	startAt := time.Now()
	for i := 0; i < len(g.txns); i += batch {
		<-tick.C
		end := min(i+batch, len(g.txns))
		items := make([]map[string]any, 0, end-i)
		for _, t := range g.txns[i:end] {
			obj := map[string]any{}
			for k, v := range t.row() {
				obj[header[k]] = v
			}
			items = append(items, obj)
		}
		var body []byte
		if batch == 1 {
			body, _ = json.Marshal(items[0])
		} else {
			body, _ = json.Marshal(items)
		}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("post: %v", err)
			continue
		}
		var v struct {
			Signals []any `json:"signals"`
			Summary struct {
				Flagged int `json:"flagged"`
			} `json:"summary"`
		}
		json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		sent += end - i
		if len(v.Signals) > 0 {
			flagged++
		}
		flagged += v.Summary.Flagged
		if sent%(rate*5) < batch {
			log.Printf("sent %d/%d (%.0f/s), flagged %d", sent, len(g.txns), float64(sent)/time.Since(startAt).Seconds(), flagged)
		}
	}
	log.Printf("done: sent %d, flagged %d", sent, flagged)
}
