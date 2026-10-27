// Package engine keeps transactions in memory and evaluates fraud rules.
//
// Each incoming transaction is scored synchronously against the real-time
// rules (microseconds, constant work per transaction). CSP-level analytics
// (split %, round-trip groups, attendance, spikes, peer anomaly, rings) are
// computed on demand from the same in-memory state and cached per version.
package engine

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// Signal is one rule hit on a transaction.
type Signal struct {
	Rule     string         `json:"rule"`
	Severity Severity       `json:"severity"`
	Weight   float64        `json:"weight"`
	Message  string         `json:"message"`
	Account  string         `json:"account,omitempty"`
	Related  []string       `json:"related_txns,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

// Alert is a stored, non-tag signal with its transaction context.
type Alert struct {
	ID     int64       `json:"id"`
	TxnID  string      `json:"txn_id"`
	Time   time.Time   `json:"timestamp"`
	CSP    string      `json:"csp_id"`
	Amount model.Money `json:"amount"`
	Kind   model.Kind  `json:"kind"`
	Signal
}

// Record is a stored transaction with its evaluation result.
type Record struct {
	model.Transaction
	Seq     int
	Day     string // YYYY-MM-DD in the engine time zone
	Month   string // YYYY-MM
	Hour    int
	Score   float64
	Flags   uint32
	Signals []Signal
}

// Verdict is the synchronous answer for one submitted transaction.
type Verdict struct {
	TxnID     string   `json:"txn_id"`
	Duplicate bool     `json:"duplicate,omitempty"`
	RiskScore float64  `json:"risk_score"`
	RiskLevel string   `json:"risk_level"`
	Action    string   `json:"action"`
	Signals   []Signal `json:"signals"`
	LatencyUS float64  `json:"latency_us"`
}

type cspAcct struct{ csp, acct string }
type cspDay struct{ csp, day string }
type acctDay struct{ acct, day string }

type splitKey struct {
	day, csp, from, to string
	kind               model.Kind
}

type splitGroup struct {
	count   int
	total   model.Money
	recs    []*Record
	flagged bool
}

type dupKey struct {
	csp, from, to string
	amount        model.Money
	kind          model.Kind
}

type destUse struct {
	count   int
	amount  model.Money
	days    map[string]struct{}
	sources map[string]struct{}
}

type flow struct {
	t   time.Time
	amt model.Money
}

type acctState struct {
	lastSeen time.Time
	n        int
	mean, m2 float64 // Welford running stats of log(amount)
	outTimes []time.Time
	inflows  []flow
	outflows []flow
	failures []time.Time
}

// cspLive accumulates alert hits per CSP as they happen.
type cspLive struct {
	byRule   map[string]int
	accounts map[string]map[string]int // rule -> account -> hits
}

// Engine is safe for concurrent use.
type Engine struct {
	mu      sync.RWMutex
	cfg     Config
	loc     *time.Location
	rules   []Rule
	ignored map[string]bool

	recs      []*Record
	byID      map[string]*Record
	strs      map[string]string
	alerts    []Alert
	autoSeq   int
	version   uint64
	onAlert   func([]Alert)
	latencies [4096]int64
	latN      int

	split    map[splitKey]*splitGroup
	graphs   map[cspDay]*dayGraph
	testDep  map[cspAcct][]*Record
	outflow  map[cspAcct][]*Record
	destCSPs map[string]map[string]struct{}
	destUse  map[cspAcct]*destUse
	acctCSPs map[string]map[string]struct{}
	accts    map[string]*acctState
	dups     map[dupKey]*Record
	nearThr  map[acctDay]int
	csps     map[string]*cspLive

	cache reportCache
}

// New creates an engine with the given configuration.
func New(cfg Config) (*Engine, error) {
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", cfg.Timezone, err)
	}
	e := &Engine{cfg: cfg, loc: loc, ignored: map[string]bool{}}
	for _, a := range cfg.IgnoreAccounts {
		e.ignored[a] = true
	}
	e.rules = append([]Rule(nil), Rules...)
	for i := range e.rules {
		if w, ok := cfg.Weights[e.rules[i].ID]; ok {
			e.rules[i].Weight = math.Max(0, math.Min(1, w))
		}
	}
	e.resetState()
	return e, nil
}

func (e *Engine) resetState() {
	e.recs = nil
	e.byID = map[string]*Record{}
	e.strs = map[string]string{}
	e.alerts = nil
	e.autoSeq = 0
	e.split = map[splitKey]*splitGroup{}
	e.graphs = map[cspDay]*dayGraph{}
	e.testDep = map[cspAcct][]*Record{}
	e.outflow = map[cspAcct][]*Record{}
	e.destCSPs = map[string]map[string]struct{}{}
	e.destUse = map[cspAcct]*destUse{}
	e.acctCSPs = map[string]map[string]struct{}{}
	e.accts = map[string]*acctState{}
	e.dups = map[dupKey]*Record{}
	e.nearThr = map[acctDay]int{}
	e.csps = map[string]*cspLive{}
	e.cache = reportCache{}
	e.version++
}

// Config returns the active configuration.
func (e *Engine) Config() Config { return e.cfg }

// Location returns the engine time zone.
func (e *Engine) Location() *time.Location { return e.loc }

// RuleCatalogue returns the rules with effective weights.
func (e *Engine) RuleCatalogue() []Rule { return append([]Rule(nil), e.rules...) }

// OnAlert registers a callback invoked (outside the lock) with new alerts.
func (e *Engine) OnAlert(f func([]Alert)) {
	e.mu.Lock()
	e.onAlert = f
	e.mu.Unlock()
}

// Ingest evaluates and stores transactions in order, returning one verdict
// per input and the transactions that were newly accepted (with ids filled).
func (e *Engine) Ingest(txns []model.Transaction) ([]Verdict, []model.Transaction) {
	verdicts := make([]Verdict, len(txns))
	accepted := make([]model.Transaction, 0, len(txns))
	var fresh []Alert

	e.mu.Lock()
	for i := range txns {
		start := time.Now()
		r, dup := e.ingestOne(txns[i])
		if !dup {
			accepted = append(accepted, r.Transaction)
			fresh = e.emitAlerts(r, fresh)
		}
		ns := time.Since(start).Nanoseconds()
		e.latencies[e.latN%len(e.latencies)] = ns
		e.latN++
		verdicts[i] = e.verdict(r, dup, ns)
	}
	if len(accepted) > 0 {
		e.version++
	}
	hook := e.onAlert
	e.mu.Unlock()

	if hook != nil && len(fresh) > 0 {
		hook(fresh)
	}
	return verdicts, accepted
}

func (e *Engine) ingestOne(t model.Transaction) (*Record, bool) {
	t.Normalize()
	if t.ID == "" {
		e.autoSeq++
		t.ID = fmt.Sprintf("AUTO-%d", e.autoSeq)
	}
	if r, ok := e.byID[t.ID]; ok {
		return r, true
	}
	t.ID = e.intern(t.ID)
	t.CSP = e.intern(t.CSP)
	t.KO = e.intern(t.KO)
	t.From = e.intern(t.From)
	t.To = e.intern(t.To)
	t.Type = e.intern(t.Type)
	t.Channel = e.intern(t.Channel)
	t.Status = e.intern(t.Status)
	t.PeerGroup = e.intern(t.PeerGroup)

	lt := t.Time.In(e.loc)
	r := &Record{Transaction: t, Seq: len(e.recs), Hour: lt.Hour()}
	r.Day = e.intern(lt.Format("2006-01-02"))
	r.Month = r.Day[:7]
	e.recs = append(e.recs, r)
	e.byID[t.ID] = r

	if t.Failed {
		e.trackFailure(r)
		return r, false
	}
	e.evaluate(r)

	keep := 1.0
	for _, s := range r.Signals {
		keep *= 1 - s.Weight
	}
	r.Score = round3(1 - keep)
	return r, false
}

func (e *Engine) emitAlerts(r *Record, out []Alert) []Alert {
	for _, s := range r.Signals {
		ru := e.rules[ruleIndex[s.Rule]]
		if ru.Tag {
			continue
		}
		a := Alert{ID: int64(len(e.alerts) + 1), TxnID: r.ID, Time: r.Time, CSP: r.CSP,
			Amount: r.Amount, Kind: r.Kind, Signal: s}
		e.alerts = append(e.alerts, a)
		out = append(out, a)

		c := e.csps[r.CSP]
		if c == nil {
			c = &cspLive{byRule: map[string]int{}, accounts: map[string]map[string]int{}}
			e.csps[r.CSP] = c
		}
		c.byRule[s.Rule]++
		if s.Account != "" {
			m := c.accounts[s.Rule]
			if m == nil {
				m = map[string]int{}
				c.accounts[s.Rule] = m
			}
			m[s.Account]++
		}
	}
	return out
}

func (e *Engine) verdict(r *Record, dup bool, ns int64) Verdict {
	v := Verdict{TxnID: r.ID, Duplicate: dup, RiskScore: r.Score, Signals: r.Signals,
		LatencyUS: math.Round(float64(ns)/10) / 100}
	if v.Signals == nil {
		v.Signals = []Signal{}
	}
	v.RiskLevel, v.Action = e.level(r.Score)
	return v
}

func (e *Engine) level(score float64) (string, string) {
	switch {
	case score >= e.cfg.BlockScore:
		return "high", "BLOCK"
	case score >= e.cfg.ReviewScore:
		return "medium", "REVIEW"
	case score > 0:
		return "low", "ALLOW"
	}
	return "none", "ALLOW"
}

// fire records a rule hit on a transaction (at most once per rule).
func (e *Engine) fire(r *Record, rule, account, msg string, related []string, details map[string]any) {
	ru := &e.rules[ruleIndex[rule]]
	if r.Flags&ru.Bit != 0 {
		return
	}
	r.Flags |= ru.Bit
	r.Signals = append(r.Signals, Signal{Rule: rule, Severity: ru.Severity, Weight: ru.Weight,
		Message: msg, Account: account, Related: related, Details: details})
}

func (e *Engine) intern(s string) string {
	if s == "" {
		return s
	}
	if v, ok := e.strs[s]; ok {
		return v
	}
	e.strs[s] = s
	return s
}

// usable reports whether an account can be tracked individually.
func (e *Engine) usable(acct string) bool {
	return acct != "" && !model.IsMasked(acct) && !e.ignored[acct]
}

func ids(recs []*Record, max int) []string {
	if len(recs) > max {
		recs = recs[len(recs)-max:]
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// ---- queries ----

// Get returns a copy of a stored record.
func (e *Engine) Get(id string) (Record, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	r, ok := e.byID[id]
	if !ok {
		return Record{}, false
	}
	return *r, true
}

// Level maps a score to risk level and action.
func (e *Engine) Level(score float64) (string, string) { return e.level(score) }

// TxnFilter selects transactions for listing.
type TxnFilter struct {
	CSP, Account, Day string
	MinScore          float64
	FlaggedOnly       bool
	Limit, Offset     int
}

// Transactions lists records newest first.
func (e *Engine) Transactions(f TxnFilter) ([]Record, int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	var out []Record
	total := 0
	for i := len(e.recs) - 1; i >= 0; i-- {
		r := e.recs[i]
		if f.CSP != "" && r.CSP != f.CSP ||
			f.Account != "" && r.From != f.Account && r.To != f.Account ||
			f.Day != "" && r.Day != f.Day ||
			r.Score < f.MinScore ||
			f.FlaggedOnly && len(r.Signals) == 0 {
			continue
		}
		total++
		if total > f.Offset && len(out) < f.Limit {
			out = append(out, *r)
		}
	}
	return out, total
}

// AlertFilter selects alerts.
type AlertFilter struct {
	Rule, CSP, Account, TxnID string
	MinSeverity               Severity
	Since                     int64 // only alerts with id > Since
	Limit, Offset             int
}

// Alerts lists alerts newest first.
func (e *Engine) Alerts(f AlertFilter) ([]Alert, int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if f.Limit <= 0 {
		f.Limit = 100
	}
	minRank := f.MinSeverity.rank()
	var out []Alert
	total := 0
	for i := len(e.alerts) - 1; i >= 0; i-- {
		a := &e.alerts[i]
		if a.ID <= f.Since {
			break
		}
		if f.Rule != "" && a.Rule != f.Rule ||
			f.CSP != "" && a.CSP != f.CSP ||
			f.Account != "" && a.Account != f.Account ||
			f.TxnID != "" && a.TxnID != f.TxnID ||
			a.Severity.rank() < minRank {
			continue
		}
		total++
		if total > f.Offset && len(out) < f.Limit {
			out = append(out, *a)
		}
	}
	return out, total
}

// Stats is a dashboard summary.
type Stats struct {
	Transactions int            `json:"transactions"`
	Failed       int            `json:"failed"`
	Amount       model.Money    `json:"amount"`
	CSPs         int            `json:"csps"`
	Accounts     int            `json:"accounts"`
	Alerts       int            `json:"alerts"`
	FlaggedTxns  int            `json:"flagged_txns"`
	HighRiskTxns int            `json:"high_risk_txns"`
	ReviewTxns   int            `json:"review_txns"`
	AlertsByRule map[string]int `json:"alerts_by_rule"`
	AlertsBySev  map[string]int `json:"alerts_by_severity"`
	Daily        []DayStat      `json:"daily"`
	LatencyP50US float64        `json:"latency_p50_us"`
	LatencyP99US float64        `json:"latency_p99_us"`
	FirstTxn     *time.Time     `json:"first_txn,omitempty"`
	LastTxn      *time.Time     `json:"last_txn,omitempty"`
	Version      uint64         `json:"version"`
}

// DayStat is per-day volume.
type DayStat struct {
	Day     string      `json:"day"`
	Txns    int         `json:"txns"`
	Amount  model.Money `json:"amount"`
	Flagged int         `json:"flagged"`
	Alerts  int         `json:"alerts"`
}

// Stats computes the dashboard summary.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s := Stats{AlertsByRule: map[string]int{}, AlertsBySev: map[string]int{}, Version: e.version,
		Transactions: len(e.recs), Alerts: len(e.alerts)}
	days := map[string]*DayStat{}
	csps := map[string]struct{}{}
	accts := map[string]struct{}{}
	var first, last time.Time
	for _, r := range e.recs {
		d := days[r.Day]
		if d == nil {
			d = &DayStat{Day: r.Day}
			days[r.Day] = d
		}
		d.Txns++
		if first.IsZero() || r.Time.Before(first) {
			first = r.Time
		}
		if r.Time.After(last) {
			last = r.Time
		}
		csps[r.CSP] = struct{}{}
		if r.From != "" {
			accts[r.From] = struct{}{}
		}
		if r.To != "" {
			accts[r.To] = struct{}{}
		}
		if r.Failed {
			s.Failed++
			continue
		}
		s.Amount += r.Amount
		d.Amount += r.Amount
		if len(r.Signals) > 0 {
			s.FlaggedTxns++
			d.Flagged++
		}
		switch lvl, _ := e.level(r.Score); lvl {
		case "high":
			s.HighRiskTxns++
		case "medium":
			s.ReviewTxns++
		}
	}
	for _, a := range e.alerts {
		s.AlertsByRule[a.Rule]++
		s.AlertsBySev[string(a.Severity)]++
		if d := days[a.Time.In(e.loc).Format("2006-01-02")]; d != nil {
			d.Alerts++
		}
	}
	s.CSPs, s.Accounts = len(csps), len(accts)
	for _, d := range days {
		s.Daily = append(s.Daily, *d)
	}
	sort.Slice(s.Daily, func(i, j int) bool { return s.Daily[i].Day < s.Daily[j].Day })
	if len(s.Daily) > 90 {
		s.Daily = s.Daily[len(s.Daily)-90:]
	}
	if !first.IsZero() {
		s.FirstTxn, s.LastTxn = &first, &last
	}
	n := e.latN
	if n > len(e.latencies) {
		n = len(e.latencies)
	}
	if n > 0 {
		l := append([]int64(nil), e.latencies[:n]...)
		sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
		s.LatencyP50US = float64(l[n/2]) / 1000
		s.LatencyP99US = float64(l[(n*99)/100]) / 1000
	}
	return s
}

// Version changes whenever new transactions are stored.
func (e *Engine) Version() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.version
}

// Len returns the number of stored transactions.
func (e *Engine) Len() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.recs)
}

// Reset drops all state.
func (e *Engine) Reset() {
	e.mu.Lock()
	e.resetState()
	e.mu.Unlock()
}

// Compact drops transactions older than cutoff and rebuilds all rule state
// from the remaining ones (alerts are re-derived). It returns the kept
// transactions so the caller can rewrite its log.
func (e *Engine) Compact(cutoff time.Time) []model.Transaction {
	e.mu.Lock()
	defer e.mu.Unlock()
	var keep []model.Transaction
	for _, r := range e.recs {
		if !r.Time.Before(cutoff) {
			keep = append(keep, r.Transaction)
		}
	}
	if len(keep) == len(e.recs) {
		return nil
	}
	e.resetState()
	for _, t := range keep {
		if r, dup := e.ingestOne(t); !dup {
			e.emitAlerts(r, nil)
		}
	}
	return keep
}
