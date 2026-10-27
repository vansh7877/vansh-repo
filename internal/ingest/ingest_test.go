package ingest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

var ist = time.FixedZone("IST", 19800)

func TestCSVAliasesAndFormats(t *testing.T) {
	in := "\xef\xbb\xbfTxn No;Txn Date;Txn Time;Agent ID;KO Code;Debit A/c;Credit A/c;Transaction Type;Amount (Rs);Charges;ON-US/OFF-US;Status;District\n" +
		"T1;27/10/2026;14:05;CSP9;KO123456789;;5001;Cash Deposit;\"₹4,950.00\";0;On Us;Success;Pune\n" +
		"T2;27-10-2026;2:05:00 pm;CSP9;KO1;5001;;Cash Withdrawal;Rs. 436;0;OFFUS;FAILED;Pune\n" +
		"T3;bad-date;;CSP9;;;;Deposit;10;;;;\n"
	res, err := ParseCSV(strings.NewReader(in), ist)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Txns) != 2 || len(res.Errors) != 1 || res.Errors[0].Row != 4 {
		t.Fatalf("got %d txns, errors %+v", len(res.Txns), res.Errors)
	}
	a, b := res.Txns[0], res.Txns[1]
	if a.ID != "T1" || a.CSP != "CSP9" || a.To != "5001" || a.Amount != model.Rupees(4950) || a.Kind != model.KindDeposit ||
		a.OffUs || a.Failed || a.PeerGroup != "Pune" || a.KO != "KO123456789" {
		t.Fatalf("row 1: %+v", a)
	}
	if want := time.Date(2026, 10, 27, 14, 5, 0, 0, ist); !a.Time.Equal(want) {
		t.Fatalf("time %v want %v", a.Time, want)
	}
	if b.Amount != model.Rupees(436) || !b.OffUs || !b.Failed || b.Kind != model.KindWithdrawal || b.Time.Hour() != 14 {
		t.Fatalf("row 2: %+v", b)
	}
}

func TestSingleAccountColumn(t *testing.T) {
	in := "csp,account,type,amount,timestamp\nC1,999,Cash Withdrawal,100,2026-10-01 10:00:00\nC1,999,Cash Deposit,100,2026-10-01T10:00:00+05:30\n"
	res, err := ParseCSV(strings.NewReader(in), ist)
	if err != nil {
		t.Fatal(err)
	}
	if res.Txns[0].From != "999" || res.Txns[1].To != "999" {
		t.Fatalf("account placement: %+v %+v", res.Txns[0], res.Txns[1])
	}
}

func TestMissingRequiredColumn(t *testing.T) {
	if _, err := ParseCSV(strings.NewReader("txn_id,amount\n1,100\n"), ist); err == nil {
		t.Fatal("expected error for missing csp column")
	}
}

func TestJSON(t *testing.T) {
	res, err := ParseJSON(strings.NewReader(`{"transactions":[{"TxnId":"J1","CSP ID":"C1","Amount":5050,"Txn Type":"Cash Withdrawal","From Account":"1","Timestamp":"2026-10-01 09:00:00"},{"csp_id":"C1"}]}`), ist)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Txns) != 1 || res.Txns[0].Amount != model.Rupees(5050) || res.Txns[0].ID != "J1" || len(res.Errors) != 1 {
		t.Fatalf("%+v %+v", res.Txns, res.Errors)
	}
}

// buildXLSX writes a minimal workbook with shared strings, numbers and an Excel date.
func buildXLSX(t *testing.T) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add("xl/workbook.xml", `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/data.xml"/></Relationships>`)
	add("xl/sharedStrings.xml", `<sst><si><t>Report</t></si><si><t>CSP</t></si><si><t>Amount</t></si><si><t>Date</t></si><si><r><t>Cash </t></r><r><t>Withdrawal</t></r></si><si><t>Type</t></si><si><t>CSP7</t></si></sst>`)
	add("xl/worksheets/data.xml", `<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c></row>
<row r="2"><c r="A2" t="s"><v>1</v></c><c r="B2" t="s"><v>2</v></c><c r="C2" t="s"><v>3</v></c><c r="D2" t="s"><v>5</v></c></row>
<row r="3"><c r="A3" t="s"><v>6</v></c><c r="B3"><v>436</v></c><c r="C3"><v>46322.5</v></c><c r="D3" t="s"><v>4</v></c></row>
<row r="4"><c r="A4" t="inlineStr"><is><t>CSP8</t></is></c><c r="C4"><v>46322</v></c><c r="B4"><v>50</v></c></row>
</sheetData></worksheet>`)
	zw.Close()
	return buf.Bytes()
}

func TestXLSX(t *testing.T) {
	data := buildXLSX(t)
	if f := DetectFormat("x.bin", "", data[:8]); f != FormatXLSX {
		t.Fatalf("detected %s", f)
	}
	res, err := Parse(bytes.NewReader(data), FormatXLSX, ist)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Txns) != 2 {
		t.Fatalf("txns %d errors %v", len(res.Txns), res.Errors)
	}
	a := res.Txns[0]
	if a.CSP != "CSP7" || a.Amount != model.Rupees(436) || a.Type != "Cash Withdrawal" ||
		a.Time.Format("2006-01-02 15:04") != "2026-10-27 12:00" {
		t.Fatalf("xlsx row: %+v (time %s)", a, a.Time)
	}
	if res.Txns[1].CSP != "CSP8" || res.Txns[1].Amount != model.Rupees(50) {
		t.Fatalf("xlsx row 2: %+v", res.Txns[1])
	}
}

func TestParseMoney(t *testing.T) {
	for in, want := range map[string]float64{"1,250.50": 1250.5, "Rs. 500": 500, "₹ 436": 436, "INR 50": 50, "12,34,567": 1234567} {
		got, err := model.ParseMoney(in)
		if err != nil || got != model.Rupees(want) {
			t.Errorf("%q: got %v %v", in, got, err)
		}
	}
	if _, err := model.ParseMoney("abc"); err == nil {
		t.Error("expected error")
	}
	if s := fmt.Sprint(model.Rupees(1234567.5)); s != "₹12,34,567.50" {
		t.Errorf("format %s", s)
	}
}
