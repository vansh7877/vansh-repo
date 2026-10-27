// Package ingest turns CSV / XLSX / JSON input into model.Transactions.
// Column names are matched loosely ("From Account", "from_acc", "Debit A/c"…)
// so sheets exported from different bank systems work without reformatting.
package ingest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// Field is a canonical transaction column.
type Field int

const (
	FID Field = iota
	FTimestamp
	FDate
	FTime
	FCSP
	FKO
	FFrom
	FTo
	FAccount
	FType
	FAmount
	FCharge
	FChannel
	FStatus
	FPeer
	numFields
)

var fieldNames = [numFields]string{
	"txn_id", "timestamp", "date", "time", "csp_id", "ko_id", "from_account", "to_account",
	"account", "txn_type", "amount", "customer_charge", "channel", "status", "peer_group",
}

func (f Field) String() string { return fieldNames[f] }

var aliases = map[string]Field{}

func init() {
	add := func(f Field, names ...string) {
		for _, n := range names {
			aliases[normHeader(n)] = f
		}
		aliases[normHeader(fieldNames[f])] = f
	}
	add(FID, "id", "txnid", "transactionid", "txnno", "transactionno", "transactionnumber", "refno",
		"referenceno", "referencenumber", "rrn", "txnref", "transactionref", "transactionreference", "utr")
	add(FTimestamp, "timestamp", "datetime", "txndatetime", "transactiondatetime", "createdat",
		"txntimestamp", "transactiontimestamp", "dateandtime", "datetimestamp")
	add(FDate, "date", "txndate", "transactiondate", "valuedate", "trandate", "postingdate")
	add(FTime, "time", "txntime", "transactiontime", "trantime")
	add(FCSP, "csp", "cspid", "cspcode", "cspno", "agentid", "agentcode", "bcid", "bcagentid", "bccode", "cspname")
	add(FKO, "ko", "koid", "kocode", "kioskid", "kiosk", "kioskcode")
	add(FFrom, "from", "fromaccount", "fromacc", "fromac", "fromaccountno", "fromaccountnumber",
		"fromacno", "debitaccount", "debitac", "debitaccountno", "sourceaccount", "remitteraccount",
		"payeraccount", "senderaccount", "sender", "draccount")
	add(FTo, "to", "toaccount", "toacc", "toac", "toaccountno", "toaccountnumber", "toacno",
		"creditaccount", "creditac", "creditaccountno", "beneficiaryaccount", "destinationaccount",
		"payeeaccount", "receiveraccount", "receiver", "craccount")
	add(FAccount, "account", "accountno", "accountnumber", "customeraccount", "acno", "acctno")
	add(FType, "type", "txntype", "transactiontype", "trantype", "transtype", "txnmode", "service")
	add(FAmount, "amount", "amt", "txnamount", "transactionamount", "amountrs", "amountinr", "txnamt")
	add(FCharge, "customercharge", "customercharges", "charge", "charges", "fee", "fees", "servicecharge", "custcharge")
	add(FChannel, "onusoffus", "onus", "offus", "channel", "onusflag", "ususflag", "onoffus")
	add(FStatus, "status", "txnstatus", "transactionstatus", "responsestatus", "result", "responsecode")
	add(FPeer, "peergroup", "region", "district", "zone", "branch", "state", "cluster")
}

func normHeader(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FieldFor resolves a header name to a canonical field.
func FieldFor(header string) (Field, bool) {
	f, ok := aliases[normHeader(header)]
	return f, ok
}

// Mapper converts rows to transactions given a header row.
type Mapper struct {
	idx [numFields]int
	tp  timeParser
}

// NewMapper builds a mapper from a header row. Amount and CSP are required.
func NewMapper(header []string, loc *time.Location) (*Mapper, error) {
	m := &Mapper{tp: timeParser{loc: loc}}
	for i := range m.idx {
		m.idx[i] = -1
	}
	for i, h := range header {
		if f, ok := FieldFor(h); ok && m.idx[f] == -1 {
			m.idx[f] = i
		}
	}
	var missing []string
	for _, f := range []Field{FAmount, FCSP} {
		if m.idx[f] == -1 {
			missing = append(missing, f.String())
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required column(s): %s (found: %s)",
			strings.Join(missing, ", "), strings.Join(header, ", "))
	}
	return m, nil
}

// Row converts one data row.
func (m *Mapper) Row(row []string) (model.Transaction, error) {
	return build(func(f Field) string {
		if i := m.idx[f]; i >= 0 && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}, &m.tp)
}

// FromMap converts a loosely keyed object (e.g. a webhook JSON body).
func FromMap(obj map[string]string, loc *time.Location) (model.Transaction, error) {
	var vals [numFields]string
	for k, v := range obj {
		if f, ok := FieldFor(k); ok && vals[f] == "" {
			vals[f] = strings.TrimSpace(v)
		}
	}
	tp := timeParser{loc: loc}
	return build(func(f Field) string { return vals[f] }, &tp)
}

func build(get func(Field) string, tp *timeParser) (model.Transaction, error) {
	var t model.Transaction
	var err error
	if t.Amount, err = model.ParseMoney(get(FAmount)); err != nil {
		return t, fmt.Errorf("amount: %w", err)
	}
	if c := get(FCharge); c != "" {
		if t.Charge, err = model.ParseMoney(c); err != nil {
			return t, fmt.Errorf("customer_charge: %w", err)
		}
	}
	t.CSP = get(FCSP)
	if t.CSP == "" {
		return t, fmt.Errorf("missing csp_id")
	}
	t.ID = get(FID)
	t.KO = get(FKO)
	t.From = get(FFrom)
	t.To = get(FTo)
	t.Type = get(FType)
	t.Channel = get(FChannel)
	t.Status = get(FStatus)
	t.PeerGroup = get(FPeer)

	ts, d, tm := get(FTimestamp), get(FDate), get(FTime)
	switch {
	case ts != "":
		t.Time, err = tp.parse(ts)
	case d != "":
		t.Time, err = tp.parseDateTime(d, tm)
	default:
		t.Time = time.Now()
	}
	if err != nil {
		return t, err
	}

	t.Normalize()
	// A single "account" column is the customer account; place it by direction.
	if acct := get(FAccount); acct != "" && t.From == "" && t.To == "" {
		if t.IsOutflow() {
			t.From = acct
		} else {
			t.To = acct
		}
		t.Normalize()
	}
	return t, nil
}

// cellString renders a decoded JSON value as text.
func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}
