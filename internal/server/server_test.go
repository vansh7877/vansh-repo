package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vansh7877/vansh-repo/internal/engine"
	"github.com/vansh7877/vansh-repo/internal/model"
	"github.com/vansh7877/vansh-repo/internal/store"
)

func newServer(t *testing.T, dir, key string) (*httptest.Server, *engine.Engine, *store.Log) {
	t.Helper()
	eng, err := engine.New(engine.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	lg, err := store.Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lg.Replay(func(b []model.Transaction) { eng.Ingest(b) }); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(New(eng, lg, Options{APIKey: key}))
	t.Cleanup(func() { ts.Close(); lg.Close() })
	return ts, eng, lg
}

func do(t *testing.T, req *http.Request, out any) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func post(t *testing.T, url, body string, out any) int {
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return do(t, req, out)
}

func get(t *testing.T, url string, out any) int {
	req, _ := http.NewRequest("GET", url, nil)
	return do(t, req, out)
}

func TestWebhookUploadAndQueries(t *testing.T) {
	dir := t.TempDir()
	ts, _, _ := newServer(t, dir, "")

	// Single transaction: instant verdict.
	var v engine.Verdict
	code := post(t, ts.URL+"/api/v1/transactions",
		`{"txn_id":"W1","timestamp":"2026-10-01 10:00:00","csp_id":"C1","to_account":"A1","txn_type":"Cash Deposit","amount":1}`, &v)
	if code != 200 || v.TxnID != "W1" {
		t.Fatalf("webhook: %d %+v", code, v)
	}
	post(t, ts.URL+"/api/v1/transactions",
		`{"txn_id":"W2","timestamp":"2026-10-01 11:00:00","csp_id":"C1","from_account":"A1","txn_type":"Cash Withdrawal","amount":9000}`, &v)
	if v.Action != "BLOCK" || len(v.Signals) == 0 || v.Signals[0].Rule != engine.RuleTestDeposit {
		t.Fatalf("expected ₹1 test-deposit block, got %+v", v)
	}
	// Retries are idempotent.
	post(t, ts.URL+"/api/v1/transactions",
		`{"txn_id":"W2","timestamp":"2026-10-01 11:00:00","csp_id":"C1","from_account":"A1","txn_type":"Cash Withdrawal","amount":9000}`, &v)
	if !v.Duplicate {
		t.Fatal("expected duplicate")
	}
	// Invalid single transaction.
	var e map[string]string
	if code := post(t, ts.URL+"/api/v1/transactions", `{"csp_id":"C1"}`, &e); code != 400 || e["error"] == "" {
		t.Fatalf("invalid: %d %v", code, e)
	}

	// Multipart CSV upload, deliberately out of order.
	csv := "txn_id,timestamp,csp_id,from_account,to_account,txn_type,amount,customer_charge\n" +
		"U3,2026-10-02 12:00:00,C2,Z,X,Fund Transfer,4800,10\n" +
		"U1,2026-10-02 10:00:00,C2,X,Y,Fund Transfer,5000,10\n" +
		"U2,2026-10-02 11:00:00,C2,Y,Z,Fund Transfer,4900,10\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "t.csv")
	fw.Write([]byte(csv))
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var up struct {
		Summary ingestSummary `json:"summary"`
	}
	if code := do(t, req, &up); code != 200 || up.Summary.Accepted != 3 || up.Summary.ByRule[engine.RuleRoundTrip] != 1 {
		t.Fatalf("upload: %d %+v", code, up.Summary)
	}

	var alerts struct {
		Total int            `json:"total"`
		Items []engine.Alert `json:"items"`
	}
	get(t, ts.URL+"/api/v1/alerts?rule=round_trip", &alerts)
	if alerts.Total != 1 || alerts.Items[0].TxnID != "U3" {
		t.Fatalf("alerts: %+v", alerts)
	}
	var csps struct {
		Items []engine.CSPReport `json:"items"`
	}
	get(t, ts.URL+"/api/v1/csps", &csps)
	if len(csps.Items) != 2 {
		t.Fatalf("csps: %+v", csps)
	}
	var detail struct {
		Report engine.CSPReport `json:"report"`
	}
	if code := get(t, ts.URL+"/api/v1/csps/C2", &detail); code != 200 || detail.Report.RoundTripCount != 3 {
		t.Fatalf("csp detail: %d %+v", code, detail.Report)
	}
	var tx map[string]any
	if code := get(t, ts.URL+"/api/v1/transactions/W2", &tx); code != 200 {
		t.Fatalf("get txn: %d", code)
	}
	if code := get(t, ts.URL+"/api/v1/transactions/nope", nil); code != 404 {
		t.Fatalf("missing txn: %d", code)
	}
	var net engine.Network
	get(t, ts.URL+"/api/v1/network", &net)
	if len(net.Cycles) != 1 {
		t.Fatalf("network: %+v", net)
	}
	resp, _ := http.Get(ts.URL + "/api/v1/export/csps.csv")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "csv") {
		t.Fatalf("export: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp, _ = http.Get(ts.URL + "/")
	if resp.StatusCode != 200 {
		t.Fatalf("dashboard: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A new server on the same data directory restores everything.
	ts.Close()
	ts2, eng2, _ := newServer(t, dir, "")
	if eng2.Len() != 5 {
		t.Fatalf("replayed %d", eng2.Len())
	}
	get(t, ts2.URL+"/api/v1/alerts?rule=test_deposit_cashout", &alerts)
	if alerts.Total != 1 {
		t.Fatalf("alerts after replay: %d", alerts.Total)
	}
}

func TestAPIKey(t *testing.T) {
	ts, _, _ := newServer(t, t.TempDir(), "secret")
	if code := get(t, ts.URL+"/api/v1/stats", nil); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/stats", nil)
	req.Header.Set("X-API-Key", "secret")
	if code := do(t, req, nil); code != 200 {
		t.Fatalf("with key: %d", code)
	}
	if code := get(t, ts.URL+"/healthz", nil); code != 200 {
		t.Fatalf("healthz should be open: %d", code)
	}
}
