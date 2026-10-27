package engine

import (
	"math"
	"sort"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// MonthStat is one CSP's activity in a calendar month.
type MonthStat struct {
	Month           string      `json:"month"`
	ActiveDays      int         `json:"active_days"`
	DaysInData      int         `json:"days_in_data"`
	Attendance      float64     `json:"attendance"`
	Count           int         `json:"count"`
	Amount          model.Money `json:"amount"`
	CountZ          float64     `json:"count_z"`
	CountFlag       string      `json:"count_flag,omitempty"` // "high" / "low"
	CountChangePct  *float64    `json:"count_change_pct,omitempty"`
	AmountChangePct *float64    `json:"amount_change_pct,omitempty"`
	SpikeLevel      string      `json:"spike_level,omitempty"` // red / amber / watch
}

// RelativeUse is a destination account a CSP sends money to repeatedly (rule 5).
type RelativeUse struct {
	Account        string      `json:"account"`
	TxnCount       int         `json:"txn_count"`
	Amount         model.Money `json:"amount"`
	DaysUsed       int         `json:"days_used"`
	SourceAccounts int         `json:"source_accounts"`
	CSPsUsingDest  int         `json:"csps_using_destination"`
}

// SubKOBreakdown is the sub-KO share per date and type (rule 7).
type SubKOBreakdown struct {
	Day         string  `json:"day"`
	Type        string  `json:"type"`
	Count       int     `json:"count"`
	SubKOCount  int     `json:"sub_ko_count"`
	CountShare  float64 `json:"count_share"`
	AmountShare float64 `json:"amount_share"`
}

// CSPReport aggregates every CSP-level rule.
type CSPReport struct {
	CSP       string `json:"csp_id"`
	PeerGroup string `json:"peer_group"`

	RiskScore float64  `json:"risk_score"` // 0..100
	RiskLevel string   `json:"risk_level"`
	Flags     []string `json:"flags"`

	Count       int         `json:"count"`
	Amount      model.Money `json:"amount"`
	FailedCount int         `json:"failed_count"`
	FirstDay    string      `json:"first_day"`
	LastDay     string      `json:"last_day"`

	SplitCount     int         `json:"split_count"`
	SplitAmount    model.Money `json:"split_amount"`
	SplitCountPct  float64     `json:"split_count_pct"`
	SplitAmountPct float64     `json:"split_amount_pct"`

	RoundTripCount     int         `json:"round_trip_count"`
	RoundTripAmount    model.Money `json:"round_trip_amount"`
	RoundTripCountPct  float64     `json:"round_trip_count_pct"`
	RoundTripAmountPct float64     `json:"round_trip_amount_pct"`
	Loops              int         `json:"loops"`
	RoundTripGroup     int         `json:"round_trip_group"`

	ExtraCount  int         `json:"extra_count"`
	ExtraAmount model.Money `json:"extra_amount"`
	ExtraEarned model.Money `json:"extra_earned"`

	TestDepositAccounts []string      `json:"test_deposit_accounts"`
	RelativeAccounts    []RelativeUse `json:"relative_accounts"`

	PMJJBYCount  int         `json:"pmjjby_count"`
	PMJJBYAmount model.Money `json:"pmjjby_amount"`

	SubKOCount       int     `json:"sub_ko_count"`
	SubKOCountShare  float64 `json:"sub_ko_count_share"`
	SubKOAmountShare float64 `json:"sub_ko_amount_share"`

	SmallCount int `json:"small_amount_count"`

	NightCount int     `json:"night_count"`
	NightPct   float64 `json:"night_pct"`

	Attendance      float64     `json:"attendance"` // latest month
	ActiveDays      int         `json:"active_days"`
	CountFlag       string      `json:"count_flag,omitempty"`
	CountChangePct  *float64    `json:"count_change_pct,omitempty"`
	AmountChangePct *float64    `json:"amount_change_pct,omitempty"`
	SpikeLevel      string      `json:"spike_level,omitempty"`
	DayChangePct    *float64    `json:"day_change_pct,omitempty"` // latest day vs previous 7-day average
	Months          []MonthStat `json:"months"`

	AnomalyScore   float64            `json:"anomaly_score"`
	AnomalyLevel   string             `json:"anomaly_level,omitempty"`
	AnomalyDrivers map[string]float64 `json:"anomaly_drivers,omitempty"`

	RingIDs      []int          `json:"ring_ids,omitempty"`
	HubAccounts  int            `json:"hub_accounts"`
	Alerts       int            `json:"alerts"`
	AlertsByRule map[string]int `json:"alerts_by_rule"`

	SubKO []SubKOBreakdown `json:"sub_ko_breakdown,omitempty"` // detail view only
}

type reportCache struct {
	version    uint64
	at         time.Time
	reports    []CSPReport
	net        *Network
	netVersion uint64
}

// CSPReports returns all CSP reports sorted by risk, cached while the data is unchanged.
func (e *Engine) CSPReports() []CSPReport {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cspReportsLocked()
}

func (e *Engine) cspReportsLocked() []CSPReport {
	c := &e.cache
	if c.reports != nil && c.version == e.version {
		return c.reports
	}
	net := e.networkLocked()
	c.reports = e.buildReports(net)
	c.version, c.at = e.version, time.Now()
	return c.reports
}

// CSPReport returns one CSP's report including the sub-KO breakdown.
func (e *Engine) CSPReport(csp string) (CSPReport, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.cspReportsLocked() {
		if r.CSP != csp {
			continue
		}
		type key struct{ day, typ string }
		agg := map[key]*SubKOBreakdown{}
		amt := map[key][2]model.Money{}
		for _, rec := range e.recs {
			if rec.CSP != csp || rec.Failed {
				continue
			}
			k := key{rec.Day, rec.Kind.String()}
			b := agg[k]
			if b == nil {
				b = &SubKOBreakdown{Day: k.day, Type: k.typ}
				agg[k] = b
			}
			a := amt[k]
			b.Count++
			a[0] += rec.Amount
			if len(rec.KO) > e.cfg.SubKOMinLength {
				b.SubKOCount++
				a[1] += rec.Amount
			}
			amt[k] = a
		}
		for k, b := range agg {
			b.CountShare = ratio(b.SubKOCount, b.Count)
			if a := amt[k]; a[0] > 0 {
				b.AmountShare = round3(float64(a[1]) / float64(a[0]))
			}
			r.SubKO = append(r.SubKO, *b)
		}
		sort.Slice(r.SubKO, func(i, j int) bool {
			if r.SubKO[i].Day != r.SubKO[j].Day {
				return r.SubKO[i].Day > r.SubKO[j].Day
			}
			return r.SubKO[i].Type < r.SubKO[j].Type
		})
		return r, true
	}
	return CSPReport{}, false
}

type cspAcc struct {
	rep        *CSPReport
	subKOAmt   model.Money
	months     map[string]*MonthStat
	monthDays  map[string]map[string]struct{}
	dayCount   map[string]int
	peerGroup  string
	attendance float64
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return round3(float64(a) / float64(b))
}

func moneyRatio(a, b model.Money) float64 {
	if b == 0 {
		return 0
	}
	return round3(float64(a) / float64(b))
}

func (e *Engine) buildReports(net *Network) []CSPReport {
	accs := map[string]*cspAcc{}
	get := func(csp string) *cspAcc {
		a := accs[csp]
		if a == nil {
			a = &cspAcc{rep: &CSPReport{CSP: csp, AlertsByRule: map[string]int{}},
				months: map[string]*MonthStat{}, monthDays: map[string]map[string]struct{}{},
				dayCount: map[string]int{}}
			accs[csp] = a
		}
		return a
	}
	globalMonthDays := map[string]map[string]struct{}{}
	globalDays := map[string]struct{}{}

	// Pass 1: per-transaction aggregates.
	for _, r := range e.recs {
		a := get(r.CSP)
		rep := a.rep
		if r.PeerGroup != "" {
			a.peerGroup = r.PeerGroup
		}
		if r.Failed {
			rep.FailedCount++
			continue
		}
		rep.Count++
		rep.Amount += r.Amount
		if rep.FirstDay == "" || r.Day < rep.FirstDay {
			rep.FirstDay = r.Day
		}
		if r.Day > rep.LastDay {
			rep.LastDay = r.Day
		}
		if r.Flags&e.rules[ruleIndex[RuleExtraCharge]].Bit != 0 {
			if _, extra, ok := e.extraCharge(&r.Transaction); ok {
				rep.ExtraCount++
				rep.ExtraAmount += r.Amount
				rep.ExtraEarned += extra
			}
		}
		if r.Flags&e.rules[ruleIndex[RulePMJJBY]].Bit != 0 {
			rep.PMJJBYCount++
			rep.PMJJBYAmount += r.Amount
		}
		if len(r.KO) > e.cfg.SubKOMinLength {
			rep.SubKOCount++
			a.subKOAmt += r.Amount
		}
		if r.Amount == e.cfg.SmallAmount {
			rep.SmallCount++
		}
		if e.isNight(r.Hour) {
			rep.NightCount++
		}
		m := a.months[r.Month]
		if m == nil {
			m = &MonthStat{Month: r.Month}
			a.months[r.Month] = m
			a.monthDays[r.Month] = map[string]struct{}{}
		}
		m.Count++
		m.Amount += r.Amount
		a.monthDays[r.Month][r.Day] = struct{}{}
		a.dayCount[r.Day]++
		if globalMonthDays[r.Month] == nil {
			globalMonthDays[r.Month] = map[string]struct{}{}
		}
		globalMonthDays[r.Month][r.Day] = struct{}{}
		globalDays[r.Day] = struct{}{}
	}

	// Split (order-independent: final group state).
	for k, g := range e.split {
		if g.flagged {
			rep := get(k.csp).rep
			for _, r := range g.recs {
				rep.SplitCount++
				rep.SplitAmount += r.Amount
			}
		}
	}
	// Round trip: transactions on loops per CSP-day graph.
	for k, g := range e.graphs {
		st := g.stats()
		if st.txns == 0 {
			continue
		}
		rep := get(k.csp).rep
		rep.RoundTripCount += st.txns
		rep.RoundTripAmount += st.amount
		rep.Loops += st.loops
	}
	// ₹1 test deposit accounts and live alert counts.
	for csp, live := range e.csps {
		rep := get(csp).rep
		for rule, n := range live.byRule {
			rep.AlertsByRule[rule] = n
			rep.Alerts += n
		}
		for acct := range live.accounts[RuleTestDeposit] {
			rep.TestDepositAccounts = append(rep.TestDepositAccounts, acct)
		}
		sort.Strings(rep.TestDepositAccounts)
		rep.HubAccounts = len(live.accounts[RuleHubAccount])
	}
	// Relatives (final state).
	for k, u := range e.destUse {
		if !e.relativeQualifies(k.acct, u) {
			continue
		}
		rep := get(k.csp).rep
		rep.RelativeAccounts = append(rep.RelativeAccounts, RelativeUse{Account: k.acct, TxnCount: u.count,
			Amount: u.amount, DaysUsed: len(u.days), SourceAccounts: len(u.sources), CSPsUsingDest: len(e.destCSPs[k.acct])})
	}

	months := sortedKeys(globalMonthDays)
	days := sortedKeys(globalDays)
	latestMonth := ""
	if len(months) > 0 {
		latestMonth = months[len(months)-1]
	}

	// Percentages, monthly series, spike/drop.
	for _, a := range accs {
		rep := a.rep
		rep.PeerGroup = a.peerGroup
		if rep.PeerGroup == "" {
			rep.PeerGroup = "ALL"
		}
		rep.SplitCountPct = pct(rep.SplitCount, rep.Count)
		rep.SplitAmountPct = pctMoney(rep.SplitAmount, rep.Amount)
		rep.RoundTripCountPct = pct(rep.RoundTripCount, rep.Count)
		rep.RoundTripAmountPct = pctMoney(rep.RoundTripAmount, rep.Amount)
		rep.SubKOCountShare = ratio(rep.SubKOCount, rep.Count)
		rep.SubKOAmountShare = moneyRatio(a.subKOAmt, rep.Amount)
		rep.NightPct = pct(rep.NightCount, rep.Count)
		sort.Slice(rep.RelativeAccounts, func(i, j int) bool {
			return rep.RelativeAccounts[i].TxnCount > rep.RelativeAccounts[j].TxnCount
		})

		var attSum float64
		var attN int
		rep.Months = make([]MonthStat, 0, len(months)) // fixed capacity: a.months points into it
		for i, mo := range months {
			m := a.months[mo]
			if m == nil {
				m = &MonthStat{Month: mo}
			}
			m.ActiveDays = len(a.monthDays[mo])
			m.DaysInData = len(globalMonthDays[mo])
			m.Attendance = ratio(m.ActiveDays, m.DaysInData)
			if m.Count > 0 || rep.FirstDay != "" && mo >= rep.FirstDay[:7] {
				attSum += m.Attendance
				attN++
			}
			// Spike/drop compares per-day rates: this month vs the pooled
			// previous months (so partial months at either end are fair).
			lo := i - e.cfg.SpikeLookback
			if lo < 0 {
				lo = 0
			}
			var pc, pa, pd float64
			for j := lo; j < i; j++ {
				pd += float64(len(globalMonthDays[months[j]]))
				if pm := a.months[months[j]]; pm != nil {
					pc += float64(pm.Count)
					pa += pm.Amount.Float()
				}
			}
			if pd > 0 && pc > 0 && pa > 0 && m.DaysInData > 0 {
				dd := float64(m.DaysInData)
				cc := (float64(m.Count)/dd - pc/pd) / (pc / pd)
				ac := (m.Amount.Float()/dd - pa/pd) / (pa / pd)
				cc, ac = round3(cc*100), round3(ac*100)
				m.CountChangePct, m.AmountChangePct = &cc, &ac
				m.SpikeLevel = e.spikeLevel(math.Max(math.Abs(cc), math.Abs(ac)) / 100)
			}
			rep.Months = append(rep.Months, *m)
			a.months[mo] = &rep.Months[len(rep.Months)-1]
		}
		if attN > 0 {
			a.attendance = attSum / float64(attN)
		}
		if lm := a.months[latestMonth]; lm != nil {
			rep.Attendance, rep.ActiveDays = lm.Attendance, lm.ActiveDays
			rep.CountChangePct, rep.AmountChangePct, rep.SpikeLevel = lm.CountChangePct, lm.AmountChangePct, lm.SpikeLevel
		}
		// Daily: latest day vs the average of the previous 7 days in the data.
		if len(days) >= 2 {
			last := days[len(days)-1]
			prev := days[max(0, len(days)-8) : len(days)-1]
			s := 0
			for _, d := range prev {
				s += a.dayCount[d]
			}
			if avg := float64(s) / float64(len(prev)); avg > 0 {
				ch := round3((float64(a.dayCount[last]) - avg) / avg * 100)
				rep.DayChangePct = &ch
			}
		}
	}

	// Rule 10: transaction count vs peers in each month.
	for _, mo := range months {
		groups := map[string][]*MonthStat{}
		for _, a := range accs {
			if m := a.months[mo]; m != nil && m.Count > 0 {
				groups[a.rep.PeerGroup] = append(groups[a.rep.PeerGroup], m)
			}
		}
		for _, ms := range groups {
			vals := make([]float64, len(ms))
			for i, m := range ms {
				vals[i] = float64(m.Count)
			}
			med, scale := robustScale(vals)
			if len(ms) < e.cfg.MinPeerGroup || scale == 0 {
				continue
			}
			for _, m := range ms {
				m.CountZ = round3((float64(m.Count) - med) / scale)
				switch {
				case m.CountZ >= e.cfg.CountZThreshold:
					m.CountFlag = "high"
				case m.CountZ <= -e.cfg.CountZThreshold:
					m.CountFlag = "low"
				}
			}
		}
	}
	for _, a := range accs {
		if lm := a.months[latestMonth]; lm != nil {
			a.rep.CountFlag = lm.CountFlag
		}
	}

	e.assignRoundTripGroups(accs)
	e.scoreAnomalies(accs)

	// Ring membership.
	if net != nil {
		for _, ring := range net.Rings {
			for _, csp := range ring.CSPs {
				if a := accs[csp]; a != nil {
					a.rep.RingIDs = append(a.rep.RingIDs, ring.ID)
				}
			}
		}
	}

	out := make([]CSPReport, 0, len(accs))
	for _, a := range accs {
		e.scoreCSP(a.rep)
		out = append(out, *a.rep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RiskScore != out[j].RiskScore {
			return out[i].RiskScore > out[j].RiskScore
		}
		return out[i].CSP < out[j].CSP
	})
	return out
}

func (e *Engine) spikeLevel(change float64) string {
	switch {
	case change > e.cfg.SpikeRed:
		return "red"
	case change >= e.cfg.SpikeAmber:
		return "amber"
	case change >= e.cfg.SpikeWatch:
		return "watch"
	}
	return ""
}

// assignRoundTripGroups ranks CSPs by loop count: top 5% Group1, 20% Group2,
// 50% Group3, 80% Group4, rest Group5. CSPs without loops are Group5.
func (e *Engine) assignRoundTripGroups(accs map[string]*cspAcc) {
	all := make([]*CSPReport, 0, len(accs))
	for _, a := range accs {
		all = append(all, a.rep)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Loops != all[j].Loops {
			return all[i].Loops > all[j].Loops
		}
		return all[i].RoundTripCount > all[j].RoundTripCount
	})
	n := float64(len(all))
	for i, r := range all {
		r.RoundTripGroup = 5
		if r.Loops == 0 {
			continue
		}
		above := float64(i) / n // share of CSPs ranked above this one
		switch {
		case above < 0.05:
			r.RoundTripGroup = 1
		case above < 0.20:
			r.RoundTripGroup = 2
		case above < 0.50:
			r.RoundTripGroup = 3
		case above < 0.80:
			r.RoundTripGroup = 4
		}
	}
}

// robustScale returns the median and a robust spread: scaled MAD, falling back
// to scaled mean absolute deviation when more than half the values are equal,
// and never below 10% of the median so tightly clustered peers (e.g. everyone
// at ~92% attendance) do not turn tiny differences into large z-scores.
func robustScale(vals []float64) (median, scale float64) {
	median, scale = rawScale(vals)
	if floor := 0.1 * math.Abs(median); scale < floor {
		scale = floor
	}
	return median, scale
}

func rawScale(vals []float64) (median, scale float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	median = quantile(s, 0.5)
	dev := make([]float64, len(s))
	var mean float64
	for i, v := range s {
		dev[i] = math.Abs(v - median)
		mean += dev[i]
	}
	mean /= float64(len(s))
	sort.Float64s(dev)
	if mad := quantile(dev, 0.5); mad > 0 {
		return median, 1.4826 * mad
	}
	return median, 1.2533 * mean
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// scoreAnomalies compares each CSP with its peer group (rule 13). Per-feature
// robust z-scores map to 0..1 via tanh(|z|/4); the CSP score is the maximum.
// Direction matters: low attendance and high split/round-trip are risky;
// count and amount are risky either way.
func (e *Engine) scoreAnomalies(accs map[string]*cspAcc) {
	type feature struct {
		name string
		dir  int // +1 high is risky, -1 low is risky, 0 both
		val  func(a *cspAcc) float64
	}
	features := []feature{
		{"txn_count", 0, func(a *cspAcc) float64 { return float64(a.rep.Count) }},
		{"amount", 0, func(a *cspAcc) float64 { return a.rep.Amount.Float() }},
		{"attendance", -1, func(a *cspAcc) float64 { return a.attendance }},
		{"split_count", 1, func(a *cspAcc) float64 { return float64(a.rep.SplitCount) }},
		{"round_trip_count", 1, func(a *cspAcc) float64 { return float64(a.rep.RoundTripCount) }},
	}
	groups := map[string][]*cspAcc{}
	for _, a := range accs {
		if a.rep.Count > 0 {
			groups[a.rep.PeerGroup] = append(groups[a.rep.PeerGroup], a)
		}
	}
	// Small peer groups fall back to comparing against everyone.
	var all []*cspAcc
	for _, g := range groups {
		all = append(all, g...)
	}
	if len(all) < e.cfg.MinPeerGroup {
		return
	}
	for _, g := range groups {
		peers := g
		if len(g) < e.cfg.MinPeerGroup {
			peers = all
		}
		for _, f := range features {
			vals := make([]float64, len(peers))
			for i, p := range peers {
				vals[i] = f.val(p)
			}
			med, scale := robustScale(vals)
			if scale == 0 {
				continue
			}
			for _, a := range g {
				z := (f.val(a) - med) / scale
				if f.dir > 0 && z < 0 || f.dir < 0 && z > 0 {
					continue
				}
				s := round3(math.Tanh(math.Abs(z) / 4))
				if s >= e.cfg.AnomalyMedium {
					if a.rep.AnomalyDrivers == nil {
						a.rep.AnomalyDrivers = map[string]float64{}
					}
					a.rep.AnomalyDrivers[f.name] = round3(z)
				}
				if s > a.rep.AnomalyScore {
					a.rep.AnomalyScore = s
				}
			}
		}
	}
	for _, a := range accs {
		switch {
		case a.rep.AnomalyScore >= e.cfg.AnomalyHigh:
			a.rep.AnomalyLevel = "high"
		case a.rep.AnomalyScore >= e.cfg.AnomalyMedium:
			a.rep.AnomalyLevel = "medium"
		}
	}
}

// scoreCSP combines the CSP-level signals into a 0..100 risk score
// (noisy-OR of weighted flags) with human-readable reasons.
func (e *Engine) scoreCSP(r *CSPReport) {
	keep := 1.0
	add := func(w float64, flag string) {
		keep *= 1 - w
		r.Flags = append(r.Flags, flag)
	}
	if r.SplitCountPct >= 2*e.cfg.SplitSharePct {
		add(0.4, "High split share")
	} else if r.SplitCountPct >= e.cfg.SplitSharePct {
		add(0.2, "Split transactions")
	}
	switch {
	case r.Loops > 0 && r.RoundTripGroup == 1:
		add(0.6, "Round trip Group 1")
	case r.Loops > 0 && r.RoundTripGroup == 2:
		add(0.45, "Round trip Group 2")
	case r.Loops > 0 && r.RoundTripGroup == 3:
		add(0.3, "Round trip Group 3")
	case r.Loops > 0:
		add(0.15, "Round trips")
	}
	if extraPct := pct(r.ExtraCount, r.Count); r.ExtraCount >= 3 && extraPct >= 2*e.cfg.ExtraChargeSharePct {
		add(0.55, "Repeated 1% extra charges")
	} else if r.ExtraCount >= 3 && extraPct >= e.cfg.ExtraChargeSharePct {
		add(0.25, "1% extra charges")
	}
	if len(r.TestDepositAccounts) > 0 {
		add(0.7, "₹1 test deposit + cash-out")
	}
	if len(r.RelativeAccounts) > 0 {
		add(0.45, "Transfers to relative/own accounts")
	}
	if r.PMJJBYCount >= 3 {
		add(0.3, "Repeated ₹436 debits")
	}
	if r.SmallCount >= 10 && pct(r.SmallCount, r.Count) >= 1 {
		add(0.2, "Many ₹50 transactions")
	}
	if r.SubKOAmountShare > e.cfg.SubKOShareThreshold || r.SubKOCountShare > e.cfg.SubKOShareThreshold {
		add(0.2, "High sub-KO share")
	}
	if r.NightPct > e.cfg.NightShareThreshold*100 {
		add(0.4, "Night transactions above 20%")
	}
	if r.Count > 0 && r.Attendance > 0 && r.Attendance < e.cfg.AttendanceMinRatio {
		add(0.2, "Low attendance")
	}
	if r.CountFlag != "" {
		add(0.2, "Unusual transaction count ("+r.CountFlag+")")
	}
	switch r.SpikeLevel {
	case "red":
		add(0.3, "Spike/drop red")
	case "amber":
		add(0.15, "Spike/drop amber")
	}
	switch r.AnomalyLevel {
	case "high":
		add(0.5, "Peer anomaly high")
	case "medium":
		add(0.3, "Peer anomaly medium")
	}
	if len(r.RingIDs) > 0 {
		add(0.4, "Part of a suspicious network")
	}
	if r.HubAccounts > 0 {
		add(0.15, "Uses hub accounts")
	}
	for _, x := range []struct {
		rule string
		w    float64
	}{{RulePassThrough, 0.35}, {RuleNearThreshold, 0.3}, {RuleFailedBurst, 0.25}, {RuleVelocity, 0.2},
		{RuleDuplicate, 0.15}, {RuleDormant, 0.15}, {RuleNightHighValue, 0.15}} {
		if r.AlertsByRule[x.rule] > 0 {
			ru, _ := RuleByID(x.rule)
			add(x.w, ru.Name)
		}
	}
	r.RiskScore = math.Round((1-keep)*1000) / 10
	switch {
	case r.RiskScore >= 80:
		r.RiskLevel = "high"
	case r.RiskScore >= 50:
		r.RiskLevel = "medium"
	case r.RiskScore > 0:
		r.RiskLevel = "low"
	default:
		r.RiskLevel = "none"
	}
	if r.Flags == nil {
		r.Flags = []string{}
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return math.Round(float64(a)/float64(b)*1000) / 10
}

func pctMoney(a, b model.Money) float64 {
	if b == 0 {
		return 0
	}
	return math.Round(float64(a)/float64(b)*1000) / 10
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
