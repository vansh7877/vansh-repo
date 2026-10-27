package engine

import (
	"fmt"
	"math"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// evaluate runs every real-time rule against a successful transaction.
// Each rule does constant or near-constant work using the indexes below.
func (e *Engine) evaluate(r *Record) {
	e.ruleSplit(r)
	e.ruleRoundTrip(r)
	e.ruleExtraCharge(r)
	e.ruleTestDeposit(r)
	e.ruleRelative(r)
	e.ruleExactAmounts(r)
	e.ruleHub(r)
	e.ruleDuplicate(r)
	e.ruleNearThreshold(r)
	e.ruleAccount(r)
}

// 1. Split transactions.
func (e *Engine) ruleSplit(r *Record) {
	if r.Amount <= 0 || e.cfg.SplitUnit <= 0 {
		return
	}
	k := splitKey{r.Day, r.CSP, r.From, r.To, r.SplitKind()}
	g := e.split[k]
	if g == nil {
		g = &splitGroup{}
		e.split[k] = g
	}
	g.count++
	g.total += r.Amount
	g.recs = append(g.recs, r)
	allowed := int((g.total + e.cfg.SplitUnit - 1) / e.cfg.SplitUnit)
	if g.count > allowed {
		g.flagged = true
		e.fire(r, RuleSplit, r.PrimaryAccount(),
			fmt.Sprintf("%d %s transactions today totalling %s for the same accounts; at most %d expected",
				g.count, r.SplitKind(), g.total, allowed),
			ids(g.recs, 20), map[string]any{"count": g.count, "total": g.total, "allowed": allowed})
	}
}

// roundTripEdge reports whether a transaction is a usable edge for loop detection.
func (e *Engine) roundTripEdge(r *Record) bool {
	return !r.OffUs && r.From != r.To && r.Amount >= e.cfg.RoundTripMinAmount &&
		e.usable(r.From) && e.usable(r.To)
}

// 2. Round trip: does this edge u→v close a loop v ⇝ u in today's CSP graph?
func (e *Engine) ruleRoundTrip(r *Record) {
	if !e.roundTripEdge(r) {
		return
	}
	k := cspDay{r.CSP, r.Day}
	g := e.graphs[k]
	if g == nil {
		g = newDayGraph()
		e.graphs[k] = g
	}
	path := g.path(r.To, r.From, e.cfg.RoundTripMaxDepth)
	g.add(r)
	if path == nil {
		return
	}
	loop := append([]string{r.From}, path...)
	var related []string
	var total model.Money = r.Amount
	for i := 0; i+1 < len(path); i++ {
		if recs := g.adj[path[i]][path[i+1]]; len(recs) > 0 {
			last := recs[len(recs)-1]
			related = append(related, last.ID)
			total += last.Amount
		}
	}
	related = append(related, r.ID)
	e.fire(r, RuleRoundTrip, r.From,
		fmt.Sprintf("Closes a %d-leg money loop %s at this CSP today", len(loop)-1, joinPath(loop)),
		related, map[string]any{"loop": loop, "legs": len(loop) - 1, "loop_amount": total})
}

func joinPath(p []string) string {
	s := ""
	for i, a := range p {
		if i > 0 {
			s += " → "
		}
		s += a
	}
	return s
}

// extraCharge returns the round base and the extra amount if the transaction
// looks like a round figure ±1% (rule 3).
func (e *Engine) extraCharge(t *model.Transaction) (base, extra model.Money, ok bool) {
	a := t.Amount
	if a <= 0 {
		return 0, 0, false
	}
	unit := 100 * model.Rupee
	if a >= 1000*model.Rupee {
		unit = 1000 * model.Rupee
	}
	switch {
	case t.IsOutflow():
		base = (a / unit) * unit
		if base >= e.cfg.ExtraChargeMinBase && base > 0 && a == base+base/100 {
			return base, base / 100, true
		}
	case t.Kind == model.KindDeposit:
		base = ((a + unit - 1) / unit) * unit
		if base >= e.cfg.ExtraChargeMinBase && a == base-base/100 {
			return base, base / 100, true
		}
	}
	return 0, 0, false
}

// 3. 1% extra charge.
func (e *Engine) ruleExtraCharge(r *Record) {
	base, extra, ok := e.extraCharge(&r.Transaction)
	if !ok {
		return
	}
	verb := "withdrawal/transfer is"
	if r.Kind == model.KindDeposit {
		verb = "deposit is"
	}
	e.fire(r, RuleExtraCharge, r.PrimaryAccount(),
		fmt.Sprintf("%s %s on a round %s: likely %s extra taken from the customer", verb, r.Amount, base, extra),
		nil, map[string]any{"base": base, "extra": extra})
}

// 4. ₹1 test deposit followed by a cash-out at the same CSP.
func (e *Engine) ruleTestDeposit(r *Record) {
	if r.Kind == model.KindDeposit && r.Amount == e.cfg.TestDepositAmount && e.usable(r.To) {
		k := cspAcct{r.CSP, r.To}
		e.testDep[k] = append(e.testDep[k], r)
		// Out-of-order input: cash-outs may already be stored.
		var later []*Record
		for _, o := range e.outflow[k] {
			if o.Time.After(r.Time) {
				later = append(later, o)
			}
		}
		if len(later) > 0 {
			e.fire(r, RuleTestDeposit, r.To,
				fmt.Sprintf("₹1 test deposit into %s was followed by %d cash-out(s) at this CSP", r.To, len(later)),
				ids(later, 20), map[string]any{"cash_outs": len(later)})
		}
		return
	}
	if !r.IsOutflow() || !e.usable(r.From) {
		return
	}
	k := cspAcct{r.CSP, r.From}
	e.outflow[k] = append(e.outflow[k], r)
	for _, d := range e.testDep[k] {
		if d.Time.Before(r.Time) {
			e.fire(r, RuleTestDeposit, r.From,
				fmt.Sprintf("Cash-out of %s from %s, which received a ₹1 test deposit at this CSP on %s",
					r.Amount, r.From, d.Time.In(e.loc).Format("02 Jan 15:04")),
				[]string{d.ID, r.ID}, map[string]any{"test_deposit_txn": d.ID})
			return
		}
	}
}

// relativeQualifies reports whether a CSP→destination usage pattern matches rule 5.
func (e *Engine) relativeQualifies(dest string, u *destUse) bool {
	return u.count >= e.cfg.RelativeMinTxns && len(u.sources) >= e.cfg.RelativeMinSources &&
		len(e.destCSPs[dest]) <= e.cfg.RelativeMaxCSPs
}

// 5. Fund transfer to relatives / own accounts.
func (e *Engine) ruleRelative(r *Record) {
	if r.OffUs || !r.IsOutflow() || !e.usable(r.To) || r.To == r.From {
		return
	}
	set := e.destCSPs[r.To]
	if set == nil {
		set = map[string]struct{}{}
		e.destCSPs[r.To] = set
	}
	set[r.CSP] = struct{}{}
	k := cspAcct{r.CSP, r.To}
	u := e.destUse[k]
	if u == nil {
		u = &destUse{days: map[string]struct{}{}, sources: map[string]struct{}{}}
		e.destUse[k] = u
	}
	u.count++
	u.amount += r.Amount
	u.days[r.Day] = struct{}{}
	if r.From != "" {
		u.sources[r.From] = struct{}{}
	}
	if e.relativeQualifies(r.To, u) {
		e.fire(r, RuleRelative, r.To,
			fmt.Sprintf("This CSP has sent %d transactions (%s) to %s from %d account(s); destination used by only %d CSP(s)",
				u.count, u.amount, r.To, len(u.sources), len(set)),
			nil, map[string]any{"txn_count": u.count, "amount": u.amount, "days_used": len(u.days),
				"source_accounts": len(u.sources), "csps_using_destination": len(set)})
	}
}

func (e *Engine) isNight(hour int) bool {
	s, en := e.cfg.NightStartHour, e.cfg.NightEndHour
	if s <= en {
		return hour >= s && hour < en
	}
	return hour >= s || hour < en
}

// 6, 7, 8, 12: exact amount and attribute tags.
func (e *Engine) ruleExactAmounts(r *Record) {
	if !r.OffUs && r.IsOutflow() && r.Amount == e.cfg.PMJJBYAmount {
		e.fire(r, RulePMJJBY, r.PrimaryAccount(),
			fmt.Sprintf("%s %s matches the PMJJBY premium amount", r.Kind, r.Amount), nil, nil)
	}
	if len(r.KO) > e.cfg.SubKOMinLength {
		e.fire(r, RuleSubKO, "", "Done through sub-KO "+r.KO, nil, nil)
	}
	if r.Amount == e.cfg.SmallAmount {
		e.fire(r, RuleSmallAmount, "", fmt.Sprintf("%s transaction", r.Amount), nil, nil)
	}
	if e.isNight(r.Hour) {
		e.fire(r, RuleNight, "", fmt.Sprintf("Night transaction at %02d:00 hrs", r.Hour), nil, nil)
		if r.Amount >= e.cfg.NightHighValue {
			e.fire(r, RuleNightHighValue, r.PrimaryAccount(),
				fmt.Sprintf("%s %s at %s", r.Kind, r.Amount, r.Time.In(e.loc).Format("15:04")), nil, nil)
		}
	}
}

// 14 (real-time part). Hub account: account newly seen at its Nth CSP.
func (e *Engine) ruleHub(r *Record) {
	for _, a := range [2]string{r.From, r.To} {
		if !e.usable(a) {
			continue
		}
		set := e.acctCSPs[a]
		if set == nil {
			set = map[string]struct{}{}
			e.acctCSPs[a] = set
		}
		if _, seen := set[r.CSP]; seen {
			continue
		}
		set[r.CSP] = struct{}{}
		if len(set) >= e.cfg.HubMinCSPs {
			e.fire(r, RuleHubAccount, a,
				fmt.Sprintf("Account %s is now linked to %d different CSPs", a, len(set)),
				nil, map[string]any{"csps": len(set)})
		}
	}
}

// Duplicate: same CSP, accounts, type and amount within a short window.
func (e *Engine) ruleDuplicate(r *Record) {
	k := dupKey{r.CSP, r.From, r.To, r.Amount, r.Kind}
	if prev := e.dups[k]; prev != nil {
		if d := r.Time.Sub(prev.Time); d >= 0 && d <= time.Duration(e.cfg.DuplicateWindow) {
			e.fire(r, RuleDuplicate, r.PrimaryAccount(),
				fmt.Sprintf("Same %s of %s repeated %s after %s", r.Kind, r.Amount, d.Round(time.Second), prev.ID),
				[]string{prev.ID, r.ID}, map[string]any{"gap_seconds": d.Seconds()})
		}
	}
	if prev := e.dups[k]; prev == nil || !r.Time.Before(prev.Time) {
		e.dups[k] = r
	}
}

// Threshold avoidance: repeated amounts just under the reporting threshold.
func (e *Engine) ruleNearThreshold(r *Record) {
	t := e.cfg.ReportingThreshold
	lo := model.Money(float64(t) * e.cfg.NearThresholdRatio)
	acct := r.PrimaryAccount()
	if t <= 0 || r.Amount < lo || r.Amount >= t || !e.usable(acct) {
		return
	}
	k := acctDay{acct, r.Day}
	e.nearThr[k]++
	if n := e.nearThr[k]; n >= 2 {
		e.fire(r, RuleNearThreshold, acct,
			fmt.Sprintf("%d transactions today on %s just below the %s threshold", n, acct, t),
			nil, map[string]any{"count_today": n})
	}
}

func (e *Engine) acct(a string) *acctState {
	s := e.accts[a]
	if s == nil {
		s = &acctState{}
		e.accts[a] = s
	}
	return s
}

func pruneTimes(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		ts = append(ts[:0], ts[i:]...)
	}
	return ts
}

func pruneFlows(fs []flow, cutoff time.Time) []flow {
	i := 0
	for i < len(fs) && fs[i].t.Before(cutoff) {
		i++
	}
	if i > 0 {
		fs = append(fs[:0], fs[i:]...)
	}
	return fs
}

// Per-account behavioural rules: velocity, pass-through, outlier, dormant, failed burst.
func (e *Engine) ruleAccount(r *Record) {
	now := r.Time

	if e.usable(r.To) && r.To != r.From && r.Amount != e.cfg.TestDepositAmount {
		in := e.acct(r.To)
		in.inflows = append(pruneFlows(in.inflows, now.Add(-time.Duration(e.cfg.PassThroughWindow))),
			flow{now, r.Amount})
	}

	if r.IsOutflow() && e.usable(r.From) {
		a := e.acct(r.From)
		vw := time.Duration(e.cfg.VelocityWindow)
		a.outTimes = append(pruneTimes(a.outTimes, now.Add(-vw)), now)
		if len(a.outTimes) > 4*e.cfg.VelocityMaxTxns+16 { // bound memory on out-of-order input
			a.outTimes = a.outTimes[len(a.outTimes)-e.cfg.VelocityMaxTxns-1:]
		}
		if n := countSince(a.outTimes, now.Add(-vw), now); n > e.cfg.VelocityMaxTxns {
			e.fire(r, RuleVelocity, r.From,
				fmt.Sprintf("%d outgoing transactions from %s within %s", n, r.From, vw), nil,
				map[string]any{"count": n, "window": vw.String()})
		}

		pw := time.Duration(e.cfg.PassThroughWindow)
		a.inflows = pruneFlows(a.inflows, now.Add(-pw))
		a.outflows = append(pruneFlows(a.outflows, now.Add(-pw)), flow{now, r.Amount})
		in, out := sumSince(a.inflows, now.Add(-pw), now), sumSince(a.outflows, now.Add(-pw), now)
		if in >= e.cfg.PassThroughMin && float64(out) >= e.cfg.PassThroughRatio*float64(in) {
			e.fire(r, RulePassThrough, r.From,
				fmt.Sprintf("%s credited to %s and %s moved out again within %s", in, r.From, out, pw), nil,
				map[string]any{"credited": in, "debited": out, "window": pw.String()})
		}
	}

	acct := r.PrimaryAccount()
	if !e.usable(acct) {
		return
	}
	a := e.acct(acct)
	// Amounts are heavy-tailed, so history is tracked on log(amount).
	amt := math.Log(math.Max(r.Amount.Float(), 1))
	if a.n >= e.cfg.OutlierMinHistory && r.Amount >= e.cfg.OutlierMinAmount {
		std := math.Sqrt(a.m2 / float64(a.n-1))
		typical := math.Exp(a.mean)
		z := 0.0
		if std > 0 {
			z = (amt - a.mean) / std
		}
		if r.Amount.Float() >= 5*typical && (z >= e.cfg.OutlierZ || std == 0) {
			e.fire(r, RuleAmountOutlier, acct,
				fmt.Sprintf("%s is far above the usual ₹%.0f for %s (%d previous txns)", r.Amount, typical, acct, a.n),
				nil, map[string]any{"typical": math.Round(typical), "z": round3(z)})
		}
	}
	if !a.lastSeen.IsZero() && r.Amount >= e.cfg.DormantMinAmount {
		if gap := now.Sub(a.lastSeen); gap >= time.Duration(e.cfg.DormantAfter) {
			e.fire(r, RuleDormant, acct,
				fmt.Sprintf("%s was inactive for %d days before this %s", acct, int(gap.Hours()/24), r.Amount),
				nil, map[string]any{"inactive_days": int(gap.Hours() / 24)})
		}
	}
	if len(a.failures) > 0 {
		fw := time.Duration(e.cfg.FailedBurstWindow)
		if n := countSince(a.failures, now.Add(-fw), now); n >= e.cfg.FailedBurstCount {
			e.fire(r, RuleFailedBurst, acct,
				fmt.Sprintf("Succeeded after %d failed attempts on %s within %s", n, acct, fw), nil,
				map[string]any{"failed_attempts": n})
		}
		a.failures = a.failures[:0]
	}

	a.n++
	d := amt - a.mean
	a.mean += d / float64(a.n)
	a.m2 += d * (amt - a.mean)
	if now.After(a.lastSeen) {
		a.lastSeen = now
	}
}

func (e *Engine) trackFailure(r *Record) {
	acct := r.PrimaryAccount()
	if !e.usable(acct) {
		return
	}
	a := e.acct(acct)
	a.failures = append(pruneTimes(a.failures, r.Time.Add(-time.Duration(e.cfg.FailedBurstWindow))), r.Time)
	if len(a.failures) > 64 {
		a.failures = a.failures[len(a.failures)-64:]
	}
}

func countSince(ts []time.Time, from, to time.Time) int {
	n := 0
	for _, t := range ts {
		if !t.Before(from) && !t.After(to) {
			n++
		}
	}
	return n
}

func sumSince(fs []flow, from, to time.Time) model.Money {
	var s model.Money
	for _, f := range fs {
		if !f.t.Before(from) && !f.t.After(to) {
			s += f.amt
		}
	}
	return s
}
