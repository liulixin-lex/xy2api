package schedquality

import "time"

const RuleVersion = "3.1"

// TTFTEvidence is the stable, flat representation stored in decision/state JSON
// and returned by the API. RuleBResult remains the in-memory assessment.
type TTFTEvidence struct {
	NLocal       *float64             `json:"n_local"`
	LimitSeconds float64              `json:"limit_s"`
	N            float64              `json:"n"`
	K            float64              `json:"k"`
	X            float64              `json:"x"`
	S            int                  `json:"s"`
	D            int                  `json:"d"`
	Tolerance    *float64             `json:"tolerance"`
	Confidence   *float64             `json:"confidence"`
	PriorMu      float64              `json:"prior_mu"`
	PeerNu       float64              `json:"peer_nu,omitempty"`
	PAbs         float64              `json:"p_abs"`
	PRel         *float64             `json:"p_rel"`
	U            *float64             `json:"u"`
	Z            *float64             `json:"z"`
	Causes       []RuleBUnknownReason `json:"causes"`
}

// NewTTFTEvidence copies evidence without retaining mutable result pointers.
// Relative-comparison fields remain empty; historical values still decode unchanged.
func NewTTFTEvidence(result RuleBResult, limitSeconds float64) *TTFTEvidence {
	e := &TTFTEvidence{
		LimitSeconds: limitSeconds,
		NLocal:       new(result.NLocal),
		N:            result.Counts.N, K: result.Counts.K, X: result.Counts.X,
		S: result.Counts.S, D: result.Counts.D,
		PriorMu: result.Prior, PAbs: result.AbsoluteProbability,
		Tolerance: new(result.Tolerance), Confidence: new(result.Confidence),
		Causes: append([]RuleBUnknownReason{}, result.UnknownReasons...),
	}
	return e
}

// SampleCounts preserves missing historical counts separately from a measured zero.
type SampleCounts struct {
	SamplesLocal *int `json:"samples_local"`
	SamplesTotal *int `json:"samples_total"`
}

func NewSampleCounts(local, total int) SampleCounts {
	return SampleCounts{SamplesLocal: &local, SamplesTotal: &total}
}

type Basis struct {
	// Recovery metadata lives in the existing evidence JSON, never in new state columns.
	OwnSampleAt60        *bool  `json:"own_sample_at_60,omitempty"`
	ReleaseReason        string `json:"release_reason,omitempty"` // qualified | no_samples | deadline
	TrialDurationSeconds int64  `json:"trial_duration_seconds,omitempty"`
	HistoryMissing       bool   `json:"history_missing,omitempty"`
	MissingDeadline      bool   `json:"missing_deadline,omitempty"`
	SampleCounts
	PeerKnown    float64          `json:"-"` // Weighted peer mass for the real assessment summary only.
	TrialSeats   *string          `json:"trial_seats"`
	RuleVersion  string           `json:"rule_version"`
	Line         Dimension        `json:"line"`
	Kind         string           `json:"kind"` // regular | trial | review | relax
	Cut          time.Time        `json:"cut,omitzero"`
	Threshold    float64          `json:"threshold"`     // 首字 L（秒）、报错上限或缓存下限
	SourceGroups []int64          `json:"source_groups"` // E2：证据来自哪些分组
	TTFT         *TTFTEvidence    `json:"ttft,omitempty"`
	Error        *ErrorAssessment `json:"error,omitempty"`
	Cache        *CacheAssessment `json:"cache,omitempty"`
	AttemptIDs   []string         `json:"-"` // 坏样本，最新的在前，最多 50 个，写入 sched_decisions.attempt_ids
}
type Decision struct {
	At     time.Time `json:",omitzero"`
	Key    LineKey
	Action Action
	Level  int
	Until  time.Time `json:",omitzero"`
	Basis  Basis
}
