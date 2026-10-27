// Package server exposes the engine over HTTP: webhook ingestion, file
// upload, query APIs, a live alert stream and the embedded dashboard.
package server

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vansh7877/vansh-repo/internal/engine"
	"github.com/vansh7877/vansh-repo/internal/ingest"
	"github.com/vansh7877/vansh-repo/internal/model"
	"github.com/vansh7877/vansh-repo/internal/store"
)

//go:embed web
var webFS embed.FS

// Options configures the server.
type Options struct {
	APIKey         string // if set, required on /api/ as X-API-Key or ?key=
	MaxUploadBytes int64
}

// Server wires HTTP handlers to the engine.
type Server struct {
	eng  *engine.Engine
	log  *store.Log
	opts Options
	hub  *hub
	mux  *http.ServeMux
}

// New creates the HTTP server.
func New(eng *engine.Engine, lg *store.Log, opts Options) *Server {
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 512 << 20
	}
	s := &Server{eng: eng, log: lg, opts: opts, hub: newHub(), mux: http.NewServeMux()}
	eng.OnAlert(s.hub.publishAlerts)

	static, _ := fs.Sub(webFS, "web")
	s.mux.Handle("GET /", http.FileServer(http.FS(static)))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "transactions": eng.Len()})
	})

	api := func(pattern string, h http.HandlerFunc) { s.mux.Handle(pattern, s.auth(h)) }
	api("POST /api/v1/transactions", s.handleIngest)
	api("POST /api/v1/webhook", s.handleIngest)
	api("GET /api/v1/transactions", s.handleListTxns)
	api("GET /api/v1/transactions/{id}", s.handleGetTxn)
	api("POST /api/v1/upload", s.handleUpload)
	api("GET /api/v1/alerts", s.handleAlerts)
	api("GET /api/v1/stats", s.handleStats)
	api("GET /api/v1/csps", s.handleCSPs)
	api("GET /api/v1/csps/{id}", s.handleCSP)
	api("GET /api/v1/network", s.handleNetwork)
	api("GET /api/v1/rules", s.handleRules)
	api("GET /api/v1/stream", s.handleStream)
	api("GET /api/v1/export/alerts.csv", s.handleExportAlerts)
	api("GET /api/v1/export/csps.csv", s.handleExportCSPs)
	api("POST /api/v1/reset", s.handleReset)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) auth(h http.HandlerFunc) http.Handler {
	if s.opts.APIKey == "" {
		return h
	}
	want := []byte(s.opts.APIKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-API-Key")
		if got == "" {
			got = r.URL.Query().Get("key")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			writeErr(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		h(w, r)
	})
}

// ---- ingestion ----

type txnView struct {
	model.Transaction
	Day       string          `json:"day"`
	RiskScore float64         `json:"risk_score"`
	RiskLevel string          `json:"risk_level"`
	Action    string          `json:"action"`
	Failed    bool            `json:"failed"`
	Signals   []engine.Signal `json:"signals"`
}

func (s *Server) view(r engine.Record) txnView {
	v := txnView{Transaction: r.Transaction, Day: r.Day, RiskScore: r.Score, Failed: r.Failed, Signals: r.Signals}
	v.RiskLevel, v.Action = s.eng.Level(r.Score)
	if v.Signals == nil {
		v.Signals = []engine.Signal{}
	}
	return v
}

type ingestSummary struct {
	Received   int               `json:"received"`
	Accepted   int               `json:"accepted"`
	Duplicates int               `json:"duplicates"`
	Invalid    int               `json:"invalid"`
	Flagged    int               `json:"flagged"`
	HighRisk   int               `json:"high_risk"`
	Review     int               `json:"review"`
	ByRule     map[string]int    `json:"signals_by_rule"`
	Errors     []ingest.RowError `json:"errors,omitempty"`
	ElapsedMS  float64           `json:"elapsed_ms"`
	PerTxnUS   float64           `json:"avg_latency_us"`
}

func (s *Server) commit(res *ingest.Result, start time.Time) ([]engine.Verdict, ingestSummary, error) {
	verdicts, accepted := s.eng.Ingest(res.Txns)
	err := s.log.Append(accepted)
	sum := ingestSummary{Received: res.Rows, Accepted: len(accepted), Invalid: len(res.Errors),
		Errors: res.Errors, ByRule: map[string]int{}}
	var lat float64
	for _, v := range verdicts {
		lat += v.LatencyUS
		if v.Duplicate {
			sum.Duplicates++
			continue
		}
		if len(v.Signals) > 0 {
			sum.Flagged++
		}
		switch v.Action {
		case "BLOCK":
			sum.HighRisk++
		case "REVIEW":
			sum.Review++
		}
		for _, sig := range v.Signals {
			sum.ByRule[sig.Rule]++
		}
	}
	if len(verdicts) > 0 {
		sum.PerTxnUS = round2(lat / float64(len(verdicts)))
	}
	sum.ElapsedMS = round2(float64(time.Since(start).Microseconds()) / 1000)
	if sum.Accepted > 0 {
		s.hub.publish("ingest", map[string]any{"accepted": sum.Accepted, "flagged": sum.Flagged})
	}
	return verdicts, sum, err
}

// handleIngest is the webhook: a JSON object returns one verdict, an array
// (or {"transactions": [...]}) returns verdicts plus a summary. CSV bodies
// are accepted too.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	format := ingest.DetectFormat("", r.Header.Get("Content-Type"), body)
	res, err := ingest.Parse(bytes.NewReader(body), format, s.eng.Location())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	single := format == ingest.FormatJSON && bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) &&
		!bytes.Contains(body, []byte(`"transactions"`))
	if single && len(res.Txns) == 0 {
		msg := "invalid transaction"
		if len(res.Errors) > 0 {
			msg = res.Errors[0].Error
		}
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	verdicts, sum, err := s.commit(res, start)
	if err != nil {
		log.Printf("log append: %v", err)
	}
	if single {
		writeJSON(w, 200, verdicts[0])
		return
	}
	writeJSON(w, 200, map[string]any{"summary": sum, "results": verdicts})
}

// handleUpload accepts a multipart file field "file" (CSV / XLSX / JSON) or
// a raw request body. Rows are sorted by time before evaluation so that
// sequence rules (₹1 then cash-out, velocity, pass-through) see real order.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	var (
		rd       io.Reader = r.Body
		name, ct           = "", r.Header.Get("Content-Type")
	)
	if strings.HasPrefix(ct, "multipart/") {
		f, hdr, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, "expected multipart field \"file\": "+err.Error())
			return
		}
		defer f.Close()
		rd, name, ct = f, hdr.Filename, hdr.Header.Get("Content-Type")
	} else if n := r.URL.Query().Get("filename"); n != "" {
		name = n
	}
	br := bufio.NewReaderSize(rd, 1<<16)
	head, _ := br.Peek(512)
	format := ingest.DetectFormat(name, ct, head)
	res, err := ingest.Parse(br, format, s.eng.Location())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sort.SliceStable(res.Txns, func(i, j int) bool { return res.Txns[i].Time.Before(res.Txns[j].Time) })
	verdicts, sum, err := s.commit(res, start)
	if err != nil {
		log.Printf("log append: %v", err)
	}
	// Return the riskiest transactions of this upload.
	type top struct {
		engine.Verdict
		CSP    string      `json:"csp_id"`
		Amount model.Money `json:"amount"`
	}
	var tops []top
	for i, v := range verdicts {
		if !v.Duplicate && v.RiskScore >= s.eng.Config().ReviewScore {
			tops = append(tops, top{Verdict: v, CSP: res.Txns[i].CSP, Amount: res.Txns[i].Amount})
		}
	}
	sort.SliceStable(tops, func(i, j int) bool { return tops[i].RiskScore > tops[j].RiskScore })
	if len(tops) > 50 {
		tops = tops[:50]
	}
	writeJSON(w, 200, map[string]any{"file": name, "format": format, "summary": sum, "top_risk": tops})
}

// ---- queries ----

func (s *Server) handleListTxns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	recs, total := s.eng.Transactions(engine.TxnFilter{
		CSP: q.Get("csp"), Account: q.Get("account"), Day: q.Get("day"),
		MinScore: atof(q.Get("min_score")), FlaggedOnly: q.Get("flagged") == "true",
		Limit: atoi(q.Get("limit"), 100), Offset: atoi(q.Get("offset"), 0),
	})
	out := make([]txnView, len(recs))
	for i, rec := range recs {
		out[i] = s.view(rec)
	}
	writeJSON(w, 200, map[string]any{"total": total, "items": out})
}

func (s *Server) handleGetTxn(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.eng.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "transaction not found")
		return
	}
	alerts, _ := s.eng.Alerts(engine.AlertFilter{TxnID: rec.ID, Limit: 100})
	writeJSON(w, 200, map[string]any{"transaction": s.view(rec), "alerts": alerts})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, total := s.eng.Alerts(alertFilter(q))
	if items == nil {
		items = []engine.Alert{}
	}
	writeJSON(w, 200, map[string]any{"total": total, "items": items})
}

func alertFilter(q map[string][]string) engine.AlertFilter {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	since, _ := strconv.ParseInt(get("since"), 10, 64)
	return engine.AlertFilter{Rule: get("rule"), CSP: get("csp"), Account: get("account"),
		TxnID: get("txn"), MinSeverity: engine.ParseSeverity(get("severity")), Since: since,
		Limit: atoi(get("limit"), 100), Offset: atoi(get("offset"), 0)}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.eng.Stats())
}

func (s *Server) handleCSPs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	reps := s.eng.CSPReports()
	level := q.Get("level")
	search := strings.ToLower(q.Get("q"))
	out := make([]engine.CSPReport, 0, len(reps))
	for _, rep := range reps {
		if level != "" && rep.RiskLevel != level || search != "" && !strings.Contains(strings.ToLower(rep.CSP), search) {
			continue
		}
		rep.Months = nil // keep the list light; the detail endpoint has them
		out = append(out, rep)
	}
	total := len(out)
	off, lim := atoi(q.Get("offset"), 0), atoi(q.Get("limit"), 500)
	if off > len(out) {
		off = len(out)
	}
	out = out[off:]
	if len(out) > lim {
		out = out[:lim]
	}
	writeJSON(w, 200, map[string]any{"total": total, "items": out})
}

func (s *Server) handleCSP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rep, ok := s.eng.CSPReport(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "CSP not found")
		return
	}
	alerts, _ := s.eng.Alerts(engine.AlertFilter{CSP: id, Limit: 200})
	recs, _ := s.eng.Transactions(engine.TxnFilter{CSP: id, FlaggedOnly: true, Limit: 100})
	txns := make([]txnView, len(recs))
	for i, rec := range recs {
		txns[i] = s.view(rec)
	}
	writeJSON(w, 200, map[string]any{"report": rep, "alerts": alerts, "flagged_txns": txns})
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.eng.Network())
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"rules": s.eng.RuleCatalogue(), "config": s.eng.Config()})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	s.eng.Reset()
	if err := s.log.Rewrite(nil); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.publish("reset", map[string]any{})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- exports ----

func (s *Server) handleExportAlerts(w http.ResponseWriter, r *http.Request) {
	f := alertFilter(r.URL.Query())
	f.Limit = 1 << 30
	items, _ := s.eng.Alerts(f)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="alerts.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"alert_id", "timestamp", "txn_id", "csp_id", "account", "amount", "kind", "rule", "severity", "message", "related_txns"})
	loc := s.eng.Location()
	for _, a := range items {
		cw.Write([]string{strconv.FormatInt(a.ID, 10), a.Time.In(loc).Format("2006-01-02 15:04:05"), a.TxnID, a.CSP,
			a.Account, fmtMoney(a.Amount), a.Kind.String(), a.Rule, string(a.Severity), a.Message, strings.Join(a.Related, " ")})
	}
	cw.Flush()
}

func (s *Server) handleExportCSPs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="csp_risk.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"csp_id", "peer_group", "risk_score", "risk_level", "flags", "txn_count", "amount",
		"split_count", "split_count_pct", "split_amount", "split_amount_pct",
		"round_trip_count", "round_trip_count_pct", "round_trip_amount", "round_trip_amount_pct", "loops", "round_trip_group",
		"extra_count", "extra_amount", "extra_earned", "test_deposit_accounts", "relative_accounts",
		"pmjjby_count", "pmjjby_amount", "sub_ko_count_share", "sub_ko_amount_share", "small_amount_count",
		"night_pct", "attendance", "active_days", "count_flag", "count_change_pct", "amount_change_pct", "spike_level",
		"anomaly_score", "anomaly_level", "rings", "alerts"})
	ptr := func(p *float64) string {
		if p == nil {
			return ""
		}
		return ftoa(*p)
	}
	for _, c := range s.eng.CSPReports() {
		rel := make([]string, len(c.RelativeAccounts))
		for i, ra := range c.RelativeAccounts {
			rel[i] = ra.Account
		}
		rings := make([]string, len(c.RingIDs))
		for i, id := range c.RingIDs {
			rings[i] = strconv.Itoa(id)
		}
		cw.Write([]string{c.CSP, c.PeerGroup, ftoa(c.RiskScore), c.RiskLevel, strings.Join(c.Flags, "; "),
			strconv.Itoa(c.Count), fmtMoney(c.Amount),
			strconv.Itoa(c.SplitCount), ftoa(c.SplitCountPct), fmtMoney(c.SplitAmount), ftoa(c.SplitAmountPct),
			strconv.Itoa(c.RoundTripCount), ftoa(c.RoundTripCountPct), fmtMoney(c.RoundTripAmount), ftoa(c.RoundTripAmountPct),
			strconv.Itoa(c.Loops), strconv.Itoa(c.RoundTripGroup),
			strconv.Itoa(c.ExtraCount), fmtMoney(c.ExtraAmount), fmtMoney(c.ExtraEarned),
			strings.Join(c.TestDepositAccounts, " "), strings.Join(rel, " "),
			strconv.Itoa(c.PMJJBYCount), fmtMoney(c.PMJJBYAmount), ftoa(c.SubKOCountShare), ftoa(c.SubKOAmountShare),
			strconv.Itoa(c.SmallCount), ftoa(c.NightPct), ftoa(c.Attendance), strconv.Itoa(c.ActiveDays), c.CountFlag,
			ptr(c.CountChangePct), ptr(c.AmountChangePct), c.SpikeLevel,
			ftoa(c.AnomalyScore), c.AnomalyLevel, strings.Join(rings, " "), strconv.Itoa(c.Alerts)})
	}
	cw.Flush()
}

// ---- live stream (Server-Sent Events) ----

type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newHub() *hub { return &hub{subs: map[chan []byte]struct{}{}} }

func (h *hub) publish(event string, v any) {
	b, _ := json.Marshal(v)
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default: // slow client: drop rather than block ingestion
		}
	}
}

func (h *hub) publishAlerts(alerts []engine.Alert) {
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	if n == 0 {
		return
	}
	recent := alerts
	if len(recent) > 25 {
		recent = recent[len(recent)-25:]
	}
	h.publish("alerts", map[string]any{"count": len(alerts), "items": recent})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan []byte, 64)
	s.hub.mu.Lock()
	s.hub.subs[ch] = struct{}{}
	s.hub.mu.Unlock()
	defer func() {
		s.hub.mu.Lock()
		delete(s.hub.subs, ch)
		s.hub.mu.Unlock()
	}()
	fmt.Fprint(w, "event: hello\ndata: {}\n\n")
	fl.Flush()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			w.Write(msg)
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return def
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func fmtMoney(m model.Money) string { return strconv.FormatFloat(m.Float(), 'f', 2, 64) }

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
