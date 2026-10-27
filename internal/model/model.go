// Package model defines the canonical transaction shape used across the app.
package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Money is an amount in paise. Integer arithmetic keeps exact-amount rules
// (₹1, ₹436, 1% overcharge) free of floating point surprises.
type Money int64

// Rupee is one rupee expressed in paise.
const Rupee Money = 100

// Rupees converts a rupee amount to Money.
func Rupees(r float64) Money { return Money(math.Round(r * 100)) }

// Float returns the amount in rupees.
func (m Money) Float() float64 { return float64(m) / 100 }

func (m Money) String() string { return formatINR(m) }

// MarshalJSON encodes money as a plain rupee number.
func (m Money) MarshalJSON() ([]byte, error) {
	return strconv.AppendFloat(nil, m.Float(), 'f', -1, 64), nil
}

// UnmarshalJSON accepts a number or a string such as "₹1,250.50".
func (m *Money) UnmarshalJSON(b []byte) error {
	v, err := ParseMoney(strings.Trim(string(b), `"`))
	if err != nil {
		return err
	}
	*m = v
	return nil
}

// ParseMoney parses amounts like "1,250.50", "Rs. 500", "₹ 436" or "INR 50".
func ParseMoney(s string) (Money, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"inr", "rs.", "rs", "₹"} {
		s = strings.ReplaceAll(s, p, "")
	}
	s = strings.NewReplacer(",", "", " ", "", " ", "").Replace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	return Rupees(f), nil
}

// formatINR renders money with Indian digit grouping, e.g. ₹12,34,567.50.
func formatINR(m Money) string {
	neg := m < 0
	if neg {
		m = -m
	}
	whole := strconv.FormatInt(int64(m/100), 10)
	frac := int64(m % 100)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString("₹")
	if len(whole) > 3 {
		head, tail := whole[:len(whole)-3], whole[len(whole)-3:]
		for i, r := range head {
			if i > 0 && (len(head)-i)%2 == 0 {
				b.WriteByte(',')
			}
			b.WriteRune(r)
		}
		b.WriteByte(',')
		b.WriteString(tail)
	} else {
		b.WriteString(whole)
	}
	if frac != 0 {
		fmt.Fprintf(&b, ".%02d", frac)
	}
	return b.String()
}

// Kind is the normalised transaction type.
type Kind uint8

const (
	KindOther Kind = iota
	KindDeposit
	KindWithdrawal
	KindTransfer
	KindLoan
	KindInitialDeposit
)

var kindNames = [...]string{"other", "deposit", "withdrawal", "transfer", "loan", "initial_deposit"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "other"
}

// MarshalJSON encodes the kind as its name.
func (k Kind) MarshalJSON() ([]byte, error) { return []byte(`"` + k.String() + `"`), nil }

// UnmarshalJSON decodes a kind name (used when replaying the log).
func (k *Kind) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	for i, n := range kindNames {
		if n == s {
			*k = Kind(i)
			return nil
		}
	}
	*k = ParseKind(s)
	return nil
}

// ParseKind maps free-form transaction type labels to a Kind.
func ParseKind(s string) Kind {
	l := strings.ToLower(s)
	has := func(subs ...string) bool {
		for _, x := range subs {
			if strings.Contains(l, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("loan"):
		return KindLoan
	case has("initial", "opening"):
		return KindInitialDeposit
	case has("withdraw", "cash out", "cashout", "cash-out") || l == "debit" || l == "dr":
		return KindWithdrawal
	case has("transfer", "remit", "fund", "neft", "imps", "rtgs", "upi"):
		return KindTransfer
	case has("deposit", "cash in", "cashin", "cash-in") || l == "credit" || l == "cr":
		return KindDeposit
	}
	return KindOther
}

// Transaction is one banking-correspondent transaction.
type Transaction struct {
	ID        string    `json:"txn_id"`
	Time      time.Time `json:"timestamp"`
	CSP       string    `json:"csp_id"`
	KO        string    `json:"ko_id,omitempty"`
	From      string    `json:"from_account,omitempty"`
	To        string    `json:"to_account,omitempty"`
	Type      string    `json:"txn_type,omitempty"`
	Amount    Money     `json:"amount"`
	Charge    Money     `json:"customer_charge"`
	Channel   string    `json:"channel,omitempty"` // ONUS / OFFUS
	Status    string    `json:"status,omitempty"`
	PeerGroup string    `json:"peer_group,omitempty"`

	Kind   Kind `json:"kind"`
	OffUs  bool `json:"-"`
	Failed bool `json:"-"`
}

// Normalize trims fields and fills the derived Kind / OffUs / Failed values.
func (t *Transaction) Normalize() {
	t.ID = strings.TrimSpace(t.ID)
	t.CSP = strings.TrimSpace(t.CSP)
	t.KO = strings.TrimSpace(t.KO)
	t.From = normAccount(t.From)
	t.To = normAccount(t.To)
	t.Type = strings.TrimSpace(t.Type)
	t.PeerGroup = strings.TrimSpace(t.PeerGroup)
	t.Kind = ParseKind(t.Type)

	ch := strings.ToUpper(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, t.Channel))
	t.Channel = ch
	t.OffUs = strings.HasPrefix(ch, "OFF") // unknown channel is treated as ONUS

	st := strings.ToLower(strings.TrimSpace(t.Status))
	t.Status = st
	switch st {
	case "", "s", "success", "successful", "succeeded", "completed", "complete", "approved", "ok", "00", "0", "y", "yes", "true":
		t.Failed = false
	default:
		t.Failed = true
	}
}

func normAccount(a string) string {
	a = strings.TrimSpace(a)
	switch strings.ToLower(a) {
	case "", "-", "na", "n/a", "null", "none", "nil", "0":
		return ""
	}
	return a
}

// SplitKind is the type used by the split rule: a Money Transfer with no
// customer charge counts as a deposit.
func (t *Transaction) SplitKind() Kind {
	if t.Kind == KindTransfer && t.Charge == 0 {
		return KindDeposit
	}
	return t.Kind
}

// IsOutflow reports whether money leaves the From account (withdrawal/transfer).
func (t *Transaction) IsOutflow() bool {
	return t.Kind == KindWithdrawal || t.Kind == KindTransfer
}

// PrimaryAccount is the customer account the transaction is "about":
// the credited account for deposits, the debited account otherwise.
func (t *Transaction) PrimaryAccount() string {
	switch t.Kind {
	case KindDeposit, KindLoan, KindInitialDeposit:
		if t.To != "" {
			return t.To
		}
		return t.From
	}
	if t.From != "" {
		return t.From
	}
	return t.To
}

// IsMasked reports whether an account number is masked (e.g. XXXXXX1234).
func IsMasked(acct string) bool {
	u := strings.ToUpper(acct)
	return strings.Contains(u, "XXX") || strings.Contains(u, "***")
}
