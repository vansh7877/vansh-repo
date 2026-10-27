package engine

// Severity of a rule hit.
type Severity string

const (
	SevInfo     Severity = "info"
	SevLow      Severity = "low"
	SevMedium   Severity = "medium"
	SevHigh     Severity = "high"
	SevCritical Severity = "critical"
)

func (s Severity) rank() int {
	switch s {
	case SevCritical:
		return 4
	case SevHigh:
		return 3
	case SevMedium:
		return 2
	case SevLow:
		return 1
	}
	return 0
}

// ParseSeverity returns the severity for a name (unknown names map to info).
func ParseSeverity(s string) Severity {
	for _, v := range []Severity{SevLow, SevMedium, SevHigh, SevCritical} {
		if string(v) == s {
			return v
		}
	}
	return SevInfo
}

// Rule ids.
const (
	RuleSplit          = "split"
	RuleRoundTrip      = "round_trip"
	RuleExtraCharge    = "extra_charge"
	RuleTestDeposit    = "test_deposit_cashout"
	RuleRelative       = "relative_transfer"
	RulePMJJBY         = "pmjjby_436"
	RuleSubKO          = "sub_ko"
	RuleSmallAmount    = "small_amount"
	RuleNight          = "night"
	RuleNightHighValue = "night_high_value"
	RuleHubAccount     = "hub_account"
	RuleVelocity       = "velocity"
	RulePassThrough    = "pass_through"
	RuleDuplicate      = "duplicate"
	RuleNearThreshold  = "threshold_avoidance"
	RuleAmountOutlier  = "amount_outlier"
	RuleDormant        = "dormant_reactivation"
	RuleFailedBurst    = "failed_burst"
)

// Rule describes one real-time transaction rule.
type Rule struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Severity    Severity `json:"severity"`
	Weight      float64  `json:"weight"`
	// Tag rules add to the score and appear in the verdict but are not stored
	// as alerts (they are too frequent on their own; CSP reports aggregate them).
	Tag bool   `json:"tag_only"`
	Bit uint32 `json:"-"`
}

// Rules is the catalogue of transaction-level rules, in evaluation order.
var Rules = []Rule{
	{ID: RuleSplit, Name: "Split transactions", Severity: SevMedium, Weight: 0.45,
		Description: "Same day + CSP + from/to pair + type has more transactions than ceil(total / ₹30,000)."},
	{ID: RuleRoundTrip, Name: "Round trip", Severity: SevHigh, Weight: 0.7,
		Description: "Transaction closes a money loop (A → B → … → A) at the same CSP on the same day (≥ ₹100 legs, ONUS, unmasked)."},
	{ID: RuleExtraCharge, Name: "1% extra charge", Severity: SevMedium, Weight: 0.4,
		Description: "Withdrawal/transfer equals a round figure + 1%, or deposit equals a round figure − 1%."},
	{ID: RuleTestDeposit, Name: "₹1 test deposit then cash-out", Severity: SevHigh, Weight: 0.8,
		Description: "Account received a ₹1 deposit and later had a withdrawal/transfer out at the same CSP."},
	{ID: RuleRelative, Name: "Transfer to relative / own account", Severity: SevMedium, Weight: 0.4,
		Description: "ONUS withdrawal/transfer to a destination used by ≤ 3 CSPs that this CSP uses repeatedly (≥ 3 txns from ≥ 2 customer accounts)."},
	{ID: RulePMJJBY, Name: "₹436 (PMJJBY premium)", Severity: SevLow, Weight: 0.3,
		Description: "Successful ONUS withdrawal/transfer of exactly ₹436."},
	{ID: RuleSubKO, Name: "Sub-KO usage", Severity: SevInfo, Weight: 0.05, Tag: true,
		Description: "Transaction done through a sub-KO (KO id longer than 8 characters)."},
	{ID: RuleSmallAmount, Name: "₹50 transaction", Severity: SevInfo, Weight: 0.05, Tag: true,
		Description: "Transaction of exactly the configured small amount (₹50)."},
	{ID: RuleNight, Name: "Night transaction", Severity: SevInfo, Weight: 0.1, Tag: true,
		Description: "Transaction between 22:00 and 06:00."},
	{ID: RuleNightHighValue, Name: "High-value night transaction", Severity: SevMedium, Weight: 0.35,
		Description: "Night transaction of ₹20,000 or more."},
	{ID: RuleHubAccount, Name: "Hub account", Severity: SevMedium, Weight: 0.45,
		Description: "Account has now been used at 5 or more different CSPs."},
	{ID: RuleVelocity, Name: "High velocity", Severity: SevMedium, Weight: 0.45,
		Description: "Account made more than 10 outgoing transactions within an hour."},
	{ID: RulePassThrough, Name: "Pass-through / mule", Severity: SevHigh, Weight: 0.65,
		Description: "≥ 80% of money credited to an account left it again within 30 minutes."},
	{ID: RuleDuplicate, Name: "Duplicate transaction", Severity: SevMedium, Weight: 0.4,
		Description: "Same CSP, accounts, type and amount repeated within 2 minutes."},
	{ID: RuleNearThreshold, Name: "Threshold avoidance", Severity: SevMedium, Weight: 0.45,
		Description: "Two or more same-day transactions just under the ₹50,000 reporting threshold."},
	{ID: RuleAmountOutlier, Name: "Amount outlier", Severity: SevMedium, Weight: 0.4,
		Description: "Amount is ≥ 5× this account's typical amount and ≥ 4 standard deviations above it (on a log scale)."},
	{ID: RuleDormant, Name: "Dormant account reactivated", Severity: SevMedium, Weight: 0.45,
		Description: "Account inactive for 180+ days suddenly transacts ₹5,000 or more."},
	{ID: RuleFailedBurst, Name: "Success after failed attempts", Severity: SevHigh, Weight: 0.55,
		Description: "Successful transaction after 3+ failed attempts on the account within 10 minutes."},
}

var ruleIndex = map[string]int{}

func init() {
	for i := range Rules {
		Rules[i].Bit = 1 << uint(i)
		ruleIndex[Rules[i].ID] = i
	}
}

// RuleByID looks up a rule.
func RuleByID(id string) (Rule, bool) {
	i, ok := ruleIndex[id]
	if !ok {
		return Rule{}, false
	}
	return Rules[i], true
}
