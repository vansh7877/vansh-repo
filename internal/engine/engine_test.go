package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

var base = time.Date(2026, 10, 1, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))

func newEngine(t testing.TB) *Engine {
	t.Helper()
	e, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

type tx struct {
	id, csp, from, to, typ string
	amt                    float64
	at                     time.Duration
	charge                 float64
	ko, channel, status    string
}

func (x tx) model() model.Transaction {
	if x.csp == "" {
		x.csp = "CSP1"
	}
	return model.Transaction{ID: x.id, CSP: x.csp, From: x.from, To: x.to, Type: x.typ,
		Amount: model.Rupees(x.amt), Charge: model.Rupees(x.charge), Time: base.Add(x.at),
		KO: x.ko, Channel: x.channel, Status: x.status}
}

func ingest(e *Engine, txs ...tx) []Verdict {
	in := make([]model.Transaction, len(txs))
	for i, x := range txs {
		if x.id == "" {
			x.id = fmt.Sprintf("T%d-%d", e.Len(), i)
		}
		in[i] = x.model()
	}
	v, _ := e.Ingest(in)
	return v
}

func has(v Verdict, rule string) bool {
	for _, s := range v.Signals {
		if s.Rule == rule {
			return true
		}
	}
	return false
}

func TestSplit(t *testing.T) {
	e := newEngine(t)
	// 3 deposits totalling 60,000 -> allowed ceil(60000/30000)=2 -> 3rd is split.
	v := ingest(e,
		tx{from: "A", to: "B", typ: "Cash Deposit", amt: 20000},
		tx{from: "A", to: "B", typ: "Cash Deposit", amt: 20000, at: time.Minute},
		tx{from: "A", to: "B", typ: "Cash Deposit", amt: 20000, at: 2 * time.Minute},
	)
	if has(v[1], RuleSplit) || !has(v[2], RuleSplit) {
		t.Fatalf("split: got %v / %v", v[1].Signals, v[2].Signals)
	}
	// Money Transfer with zero charge joins the deposit group.
	v = ingest(e, tx{from: "A", to: "B", typ: "Money Transfer", amt: 100, at: 3 * time.Minute})
	if !has(v[0], RuleSplit) {
		t.Fatal("zero-charge money transfer should count as a deposit for splits")
	}
	r := e.CSPReports()[0]
	if r.SplitCount != 4 || r.SplitCountPct != 100 {
		t.Fatalf("split report: %+v", r)
	}
}

func TestRoundTrip(t *testing.T) {
	e := newEngine(t)
	v := ingest(e,
		tx{from: "A", to: "B", typ: "Fund Transfer", amt: 5000, charge: 10},
		tx{from: "B", to: "C", typ: "Fund Transfer", amt: 4900, charge: 10, at: time.Hour},
		tx{from: "C", to: "A", typ: "Fund Transfer", amt: 4800, charge: 10, at: 2 * time.Hour},
		tx{from: "D", to: "E", typ: "Fund Transfer", amt: 50, charge: 10}, // below ₹100
		tx{from: "E", to: "D", typ: "Fund Transfer", amt: 50, charge: 10}, // below ₹100
		tx{from: "XXXXXX12", to: "F", typ: "Fund Transfer", amt: 500},     // masked
		tx{from: "F", to: "XXXXXX12", typ: "Fund Transfer", amt: 500},     // masked
		tx{from: "G", to: "H", typ: "Fund Transfer", amt: 500, channel: "OFFUS"},
		tx{from: "H", to: "G", typ: "Fund Transfer", amt: 500, channel: "OFFUS"},
	)
	if !has(v[2], RuleRoundTrip) || has(v[1], RuleRoundTrip) {
		t.Fatal("expected loop on third leg")
	}
	for _, i := range []int{4, 6, 8} {
		if has(v[i], RuleRoundTrip) {
			t.Fatalf("txn %d should be excluded from round trip", i)
		}
	}
	r := e.CSPReports()[0]
	if r.RoundTripCount != 3 || r.Loops != 1 || r.RoundTripGroup != 1 {
		t.Fatalf("round trip report: count=%d loops=%d group=%d", r.RoundTripCount, r.Loops, r.RoundTripGroup)
	}
	// Different day: no loop.
	e2 := newEngine(t)
	v = ingest(e2,
		tx{from: "A", to: "B", typ: "Fund Transfer", amt: 5000},
		tx{from: "B", to: "A", typ: "Fund Transfer", amt: 5000, at: 30 * time.Hour},
	)
	if has(v[1], RuleRoundTrip) {
		t.Fatal("loops are per day")
	}
}

func TestExtraCharge(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		typ  string
		amt  float64
		want bool
	}{
		{"Cash Withdrawal", 5050, true},
		{"Cash Deposit", 4950, true},
		{"Cash Withdrawal", 909, true},
		{"Cash Deposit", 990, true},
		{"Fund Transfer", 10100, true},
		{"Cash Withdrawal", 5000, false},
		{"Cash Withdrawal", 5051, false},
		{"Cash Deposit", 5050, false},
		{"Cash Withdrawal", 50.5, false}, // base below minimum
	}
	for i, c := range cases {
		v := ingest(e, tx{csp: fmt.Sprint("X", i), from: fmt.Sprint("acc", i), to: fmt.Sprint("dst", i), typ: c.typ, amt: c.amt})
		if has(v[0], RuleExtraCharge) != c.want {
			t.Errorf("%s %.2f: got %v want %v", c.typ, c.amt, !c.want, c.want)
		}
	}
	for _, r := range e.CSPReports() {
		if r.CSP == "X0" && (r.ExtraCount != 1 || r.ExtraEarned != model.Rupees(50)) {
			t.Fatalf("extra report: %+v", r)
		}
	}
}

func TestTestDepositCashOut(t *testing.T) {
	e := newEngine(t)
	v := ingest(e,
		tx{to: "ACC1", typ: "Cash Deposit", amt: 1},
		tx{from: "ACC1", typ: "Cash Withdrawal", amt: 9000, at: time.Hour},
		tx{csp: "CSP2", from: "ACC1", typ: "Cash Withdrawal", amt: 9000, at: 2 * time.Hour},
		tx{to: "ACC2", typ: "Loan Disbursement", amt: 1},
		tx{from: "ACC2", typ: "Cash Withdrawal", amt: 500, at: time.Hour},
	)
	if !has(v[1], RuleTestDeposit) {
		t.Fatal("cash-out after ₹1 deposit not flagged")
	}
	if has(v[2], RuleTestDeposit) {
		t.Fatal("different CSP must not be flagged")
	}
	if has(v[4], RuleTestDeposit) {
		t.Fatal("loan ₹1 must not count")
	}
	// Out of order: withdrawal stored first, then the earlier ₹1 deposit.
	e2 := newEngine(t)
	ingest(e2, tx{from: "ACC9", typ: "Cash Withdrawal", amt: 700, at: time.Hour})
	v = ingest(e2, tx{to: "ACC9", typ: "Cash Deposit", amt: 1})
	if !has(v[0], RuleTestDeposit) {
		t.Fatal("out-of-order test deposit not detected")
	}
	if r := e.CSPReports(); len(r[0].TestDepositAccounts) != 1 {
		t.Fatalf("report accounts: %v", r[0].TestDepositAccounts)
	}
}

func TestRelativeTransfer(t *testing.T) {
	e := newEngine(t)
	var v []Verdict
	for i := 0; i < 3; i++ {
		v = ingest(e, tx{from: fmt.Sprint("CUST", i), to: "RELATIVE", typ: "Fund Transfer", amt: 2000, charge: 10, at: time.Duration(i) * time.Hour})
	}
	if !has(v[0], RuleRelative) {
		t.Fatal("third transfer to same destination should flag")
	}
	// Destination used by many CSPs is not a relative account.
	e2 := newEngine(t)
	for i := 0; i < 6; i++ {
		v = ingest(e2, tx{csp: fmt.Sprint("C", i%5), from: fmt.Sprint("CUST", i), to: "MERCHANT", typ: "Fund Transfer", amt: 2000, charge: 10})
	}
	if has(v[0], RuleRelative) {
		t.Fatal("popular destination should not flag")
	}
}

func TestExactAmountsAndTags(t *testing.T) {
	e := newEngine(t)
	v := ingest(e,
		tx{from: "A", typ: "Cash Withdrawal", amt: 436},
		tx{from: "A", typ: "Cash Withdrawal", amt: 436, channel: "OFF-US", at: time.Minute},
		tx{from: "B", typ: "Cash Withdrawal", amt: 50, ko: "KO1234567", at: 13 * time.Hour}, // 23:00
		tx{from: "C", typ: "Cash Withdrawal", amt: 25000, at: 14 * time.Hour},               // 00:00
	)
	if !has(v[0], RulePMJJBY) || has(v[1], RulePMJJBY) {
		t.Fatal("₹436 rule")
	}
	for _, r := range []string{RuleSmallAmount, RuleSubKO, RuleNight} {
		if !has(v[2], r) {
			t.Fatalf("expected %s", r)
		}
	}
	if !has(v[3], RuleNightHighValue) {
		t.Fatal("night high value")
	}
	// Tags are not stored as alerts.
	if _, n := e.Alerts(AlertFilter{Rule: RuleSmallAmount}); n != 0 {
		t.Fatal("tag rule stored as alert")
	}
}

func TestHubAndVelocityAndPassThrough(t *testing.T) {
	e := newEngine(t)
	var v []Verdict
	for i := 0; i < 5; i++ {
		v = ingest(e, tx{csp: fmt.Sprint("C", i), from: "HUB", to: fmt.Sprint("D", i), typ: "Fund Transfer", amt: 1000, charge: 5, at: time.Duration(i) * time.Hour})
	}
	if !has(v[0], RuleHubAccount) {
		t.Fatal("hub at 5th CSP")
	}

	e = newEngine(t)
	for i := 0; i < 11; i++ {
		v = ingest(e, tx{from: "FAST", typ: "Cash Withdrawal", amt: float64(100 + i), at: time.Duration(i) * time.Minute})
	}
	if !has(v[0], RuleVelocity) {
		t.Fatal("11 withdrawals in 11 minutes")
	}

	e = newEngine(t)
	v = ingest(e,
		tx{from: "SRC", to: "MULE", typ: "Fund Transfer", amt: 20000, charge: 10},
		tx{from: "MULE", typ: "Cash Withdrawal", amt: 10000, at: 5 * time.Minute},
		tx{from: "MULE", to: "OUT", typ: "Fund Transfer", amt: 8000, charge: 10, at: 10 * time.Minute},
	)
	if has(v[1], RulePassThrough) || !has(v[2], RulePassThrough) {
		t.Fatal("pass-through at 90% out")
	}
}

func TestDuplicateNearThresholdFailedBurst(t *testing.T) {
	e := newEngine(t)
	v := ingest(e,
		tx{from: "A", to: "B", typ: "Fund Transfer", amt: 1234, charge: 5},
		tx{from: "A", to: "B", typ: "Fund Transfer", amt: 1234, charge: 5, at: time.Minute},
		tx{from: "A", to: "B", typ: "Fund Transfer", amt: 1234, charge: 5, at: 10 * time.Minute},
	)
	if !has(v[1], RuleDuplicate) || has(v[2], RuleDuplicate) {
		t.Fatal("duplicate within 2 minutes only")
	}
	v = ingest(e,
		tx{to: "N", typ: "Cash Deposit", amt: 49000},
		tx{to: "N", typ: "Cash Deposit", amt: 48500, at: 3 * time.Hour},
	)
	if !has(v[1], RuleNearThreshold) {
		t.Fatal("near threshold")
	}
	v = ingest(e,
		tx{from: "P", typ: "Cash Withdrawal", amt: 5000, status: "FAILED"},
		tx{from: "P", typ: "Cash Withdrawal", amt: 5000, status: "FAILED", at: time.Minute},
		tx{from: "P", typ: "Cash Withdrawal", amt: 5000, status: "FAILED", at: 2 * time.Minute},
		tx{from: "P", typ: "Cash Withdrawal", amt: 5000, at: 3 * time.Minute},
	)
	if !has(v[3], RuleFailedBurst) || v[0].RiskScore != 0 {
		t.Fatal("failed burst")
	}
}

func TestOutlierAndDormant(t *testing.T) {
	e := newEngine(t)
	for i := 0; i < 6; i++ {
		ingest(e, tx{from: "O", typ: "Cash Withdrawal", amt: float64(500 + 10*i), at: time.Duration(i) * 24 * time.Hour})
	}
	v := ingest(e, tx{from: "O", typ: "Cash Withdrawal", amt: 40000, at: 7 * 24 * time.Hour})
	if !has(v[0], RuleAmountOutlier) {
		t.Fatal("outlier")
	}
	v = ingest(e, tx{from: "O", typ: "Cash Withdrawal", amt: 6000, at: 200 * 24 * time.Hour})
	if !has(v[0], RuleDormant) {
		t.Fatal("dormant")
	}
}

func TestDuplicateIDIsIdempotent(t *testing.T) {
	e := newEngine(t)
	ingest(e, tx{id: "SAME", from: "A", typ: "Cash Withdrawal", amt: 436})
	v := ingest(e, tx{id: "SAME", from: "A", typ: "Cash Withdrawal", amt: 436})
	if !v[0].Duplicate || e.Len() != 1 || !has(v[0], RulePMJJBY) {
		t.Fatalf("duplicate id: %+v", v[0])
	}
}

func TestCSPLevelRules(t *testing.T) {
	e := newEngine(t)
	var txs []tx
	// 10 normal CSPs active every day for two months, plus outliers.
	for d := 0; d < 61; d++ {
		at := time.Duration(d) * 24 * time.Hour
		for c := 0; c < 10; c++ {
			for k := 0; k < 5; k++ {
				txs = append(txs, tx{csp: fmt.Sprint("N", c), from: fmt.Sprintf("N%d-%d", c, k), typ: "Cash Withdrawal",
					amt: float64(1000 + 37*k + c), at: at + time.Duration(k)*time.Hour})
			}
		}
		// NIGHT: mostly night transactions.
		txs = append(txs, tx{csp: "NIGHT", from: "NA", typ: "Cash Withdrawal", amt: 1200, at: at + 13*time.Hour})
		// LAZY: active once every 6 days.
		if d%6 == 0 {
			txs = append(txs, tx{csp: "LAZY", from: "LA", typ: "Cash Withdrawal", amt: 1100, at: at})
		}
		// SPIKE: 5/day in October, 15/day in November.
		n := 5
		if d >= 31 {
			n = 15
		}
		for k := 0; k < n; k++ {
			txs = append(txs, tx{csp: "SPIKE", from: fmt.Sprint("SP", k), typ: "Cash Withdrawal", amt: 1300, at: at + time.Duration(k)*time.Minute})
		}
	}
	ingest(e, txs...)
	byID := map[string]CSPReport{}
	for _, r := range e.CSPReports() {
		byID[r.CSP] = r
	}
	if r := byID["NIGHT"]; r.NightPct != 100 || !contains(r.Flags, "Night transactions above 20%") {
		t.Errorf("night: %v %v", r.NightPct, r.Flags)
	}
	if r := byID["LAZY"]; r.Attendance >= 0.5 || !contains(r.Flags, "Low attendance") {
		t.Errorf("attendance: %v %v", r.Attendance, r.Flags)
	}
	if r := byID["SPIKE"]; r.SpikeLevel != "red" || r.CountChangePct == nil || *r.CountChangePct < 100 {
		t.Errorf("spike: %v %v", r.SpikeLevel, r.CountChangePct)
	}
	if r := byID["LAZY"]; r.AnomalyLevel == "" {
		t.Errorf("anomaly expected for LAZY: %+v", r.AnomalyDrivers)
	}
	if r := byID["N1"]; r.AnomalyLevel != "" || r.SpikeLevel != "" {
		t.Errorf("normal CSP flagged: %v %v %v", r.AnomalyLevel, r.AnomalyDrivers, r.SpikeLevel)
	}
}

func TestNetworkRings(t *testing.T) {
	e := newEngine(t)
	ingest(e,
		tx{csp: "C1", from: "A", to: "B", typ: "Fund Transfer", amt: 5000, charge: 5},
		tx{csp: "C2", from: "B", to: "C", typ: "Fund Transfer", amt: 5000, charge: 5, at: 24 * time.Hour},
		tx{csp: "C3", from: "C", to: "A", typ: "Fund Transfer", amt: 5000, charge: 5, at: 48 * time.Hour},
	)
	n := e.Network()
	if len(n.Cycles) != 1 || !n.Cycles[0].CrossCSP || len(n.Cycles[0].CSPs) != 3 {
		t.Fatalf("cycles: %+v", n.Cycles)
	}
	if len(n.Rings) != 1 || len(n.Rings[0].CSPs) != 3 {
		t.Fatalf("rings: %+v", n.Rings)
	}
}

func TestCompact(t *testing.T) {
	e := newEngine(t)
	ingest(e,
		tx{from: "A", typ: "Cash Withdrawal", amt: 436},
		tx{from: "A", typ: "Cash Withdrawal", amt: 436, at: 100 * 24 * time.Hour},
	)
	kept := e.Compact(base.Add(50 * 24 * time.Hour))
	if len(kept) != 1 || e.Len() != 1 {
		t.Fatalf("compact kept %d", len(kept))
	}
	if _, n := e.Alerts(AlertFilter{}); n != 1 {
		t.Fatalf("alerts after compact: %d", n)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func BenchmarkIngest(b *testing.B) {
	e := newEngine(b)
	txs := make([]model.Transaction, b.N)
	for i := range txs {
		txs[i] = model.Transaction{ID: fmt.Sprint("B", i), CSP: fmt.Sprint("CSP", i%500),
			From: fmt.Sprint("ACC", i%20000), To: fmt.Sprint("ACC", (i*7)%20000), Type: "Fund Transfer",
			Amount: model.Rupees(float64(100 + i%9000)), Charge: model.Rupees(5),
			Time: base.Add(time.Duration(i) * time.Second)}
	}
	b.ResetTimer()
	for i := range txs {
		e.Ingest(txs[i : i+1])
	}
}
