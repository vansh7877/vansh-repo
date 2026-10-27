package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/vansh7877/vansh-repo/internal/model"
)

// Duration is a time.Duration that reads/writes as "30m", "1h" in JSON.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}

// Config holds every threshold used by the rules. All amounts are rupees in JSON.
type Config struct {
	Timezone string `json:"timezone"`

	// Accounts excluded from graph/account rules (e.g. CSP settlement or pool accounts).
	IgnoreAccounts []string `json:"ignore_accounts"`

	SplitUnit model.Money `json:"split_unit"` // 1. split: count > ceil(total / unit)

	RoundTripMinAmount model.Money `json:"round_trip_min_amount"` // 2. round trip
	RoundTripMaxDepth  int         `json:"round_trip_max_depth"`

	ExtraChargeMinBase model.Money `json:"extra_charge_min_base"` // 3. 1% overcharge

	TestDepositAmount model.Money `json:"test_deposit_amount"` // 4. ₹1 test deposit

	RelativeMaxCSPs    int `json:"relative_max_csps"` // 5. fund transfer to relatives
	RelativeMinTxns    int `json:"relative_min_txns"`
	RelativeMinSources int `json:"relative_min_sources"` // distinct customer accounts paying in

	PMJJBYAmount model.Money `json:"pmjjby_amount"` // 6. ₹436

	ExtraChargeSharePct float64 `json:"extra_charge_share_pct"` // CSP flag when extra-charge txns exceed this % of its txns
	SplitSharePct       float64 `json:"split_share_pct"`        // CSP flag when split txns exceed this %

	SubKOMinLength      int     `json:"sub_ko_min_length"` // 7. sub-KO = KO id longer than this
	SubKOShareThreshold float64 `json:"sub_ko_share_threshold"`

	SmallAmount model.Money `json:"small_amount"` // 8. ₹50

	AttendanceMinRatio float64 `json:"attendance_min_ratio"` // 9. active days / days in data

	CountZThreshold float64 `json:"count_z_threshold"` // 10. txn count vs peers

	SpikeRed      float64 `json:"spike_red"` // 11. spike / drop
	SpikeAmber    float64 `json:"spike_amber"`
	SpikeWatch    float64 `json:"spike_watch"`
	SpikeLookback int     `json:"spike_lookback_periods"`

	NightStartHour      int         `json:"night_start_hour"` // 12. night
	NightEndHour        int         `json:"night_end_hour"`
	NightShareThreshold float64     `json:"night_share_threshold"`
	NightHighValue      model.Money `json:"night_high_value"`

	AnomalyHigh   float64 `json:"anomaly_high"` // 13. anomaly vs peers
	AnomalyMedium float64 `json:"anomaly_medium"`
	MinPeerGroup  int     `json:"min_peer_group"`

	HubMinCSPs int `json:"hub_min_csps"` // 14. network

	// Additional real-time rules.
	VelocityWindow     Duration    `json:"velocity_window"`
	VelocityMaxTxns    int         `json:"velocity_max_txns"`
	PassThroughWindow  Duration    `json:"pass_through_window"`
	PassThroughRatio   float64     `json:"pass_through_ratio"`
	PassThroughMin     model.Money `json:"pass_through_min"`
	DuplicateWindow    Duration    `json:"duplicate_window"`
	ReportingThreshold model.Money `json:"reporting_threshold"`
	NearThresholdRatio float64     `json:"near_threshold_ratio"`
	OutlierMinHistory  int         `json:"outlier_min_history"`
	OutlierZ           float64     `json:"outlier_z"`
	OutlierMinAmount   model.Money `json:"outlier_min_amount"`
	DormantAfter       Duration    `json:"dormant_after"`
	DormantMinAmount   model.Money `json:"dormant_min_amount"`
	FailedBurstCount   int         `json:"failed_burst_count"`
	FailedBurstWindow  Duration    `json:"failed_burst_window"`

	// Verdict thresholds on the 0..1 transaction risk score.
	ReviewScore float64 `json:"review_score"`
	BlockScore  float64 `json:"block_score"`

	// Optional per-rule weight overrides (rule id -> 0..1).
	Weights map[string]float64 `json:"weights,omitempty"`
}

// DefaultConfig returns the thresholds described in the README.
func DefaultConfig() Config {
	r := model.Rupees
	return Config{
		Timezone:            "Asia/Kolkata",
		IgnoreAccounts:      []string{},
		SplitUnit:           r(30000),
		RoundTripMinAmount:  r(100),
		RoundTripMaxDepth:   8,
		ExtraChargeMinBase:  r(100),
		TestDepositAmount:   r(1),
		RelativeMaxCSPs:     3,
		RelativeMinTxns:     3,
		RelativeMinSources:  2,
		PMJJBYAmount:        r(436),
		ExtraChargeSharePct: 0.5,
		SplitSharePct:       5,
		SubKOMinLength:      8,
		SubKOShareThreshold: 0.5,
		SmallAmount:         r(50),
		AttendanceMinRatio:  0.5,
		CountZThreshold:     3,
		SpikeRed:            0.20,
		SpikeAmber:          0.15,
		SpikeWatch:          0.10,
		SpikeLookback:       3,
		NightStartHour:      22,
		NightEndHour:        6,
		NightShareThreshold: 0.20,
		NightHighValue:      r(20000),
		AnomalyHigh:         0.8,
		AnomalyMedium:       0.6,
		MinPeerGroup:        5,
		HubMinCSPs:          5,
		VelocityWindow:      Duration(time.Hour),
		VelocityMaxTxns:     10,
		PassThroughWindow:   Duration(30 * time.Minute),
		PassThroughRatio:    0.8,
		PassThroughMin:      r(5000),
		DuplicateWindow:     Duration(2 * time.Minute),
		ReportingThreshold:  r(50000),
		NearThresholdRatio:  0.9,
		OutlierMinHistory:   5,
		OutlierZ:            4,
		OutlierMinAmount:    r(10000),
		DormantAfter:        Duration(180 * 24 * time.Hour),
		DormantMinAmount:    r(5000),
		FailedBurstCount:    3,
		FailedBurstWindow:   Duration(10 * time.Minute),
		ReviewScore:         0.5,
		BlockScore:          0.8,
	}
}

// LoadConfig overlays a JSON file on top of the defaults.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}
