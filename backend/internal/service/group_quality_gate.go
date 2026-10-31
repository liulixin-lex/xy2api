package service

// GroupQualityGateTier values are used when low-upstream-rate priority is enabled.
const (
	GroupQualityGateTierFast = iota
	GroupQualityGateTierTrial
	GroupQualityGateTierOther
)

// GroupQualityGatePolicy configures quality evaluation for one group.
type GroupQualityGatePolicy struct {
	Enabled           bool    `json:"enabled"`
	MaxErrorRate      float64 `json:"max_error_rate"`
	MaxWarmTTFTP90Ms  int     `json:"max_warm_ttft_p90_ms"`
	FastWarmTTFTP90Ms int     `json:"fast_warm_ttft_p90_ms"`
	PinnedAccountIDs  []int64 `json:"pinned_account_ids"`
}

// GroupQualityMemberResult keeps the independent quality and availability facts for a group member.
type GroupQualityMemberResult struct {
	Usable    bool
	Passing   bool
	Pinned    bool
	GuardHeld bool
	State     string
	Tier      int
}

// GroupQualityGateView exposes the immutable decision view consumed by scheduling,
// fallback availability and operations availability.
type GroupQualityGateView interface {
	// Vetoed reports whether quality state currently excludes an account from a group.
	Vetoed(groupID, accountID int64) bool
	// OrderTier returns the account's ordering tier for a group.
	OrderTier(groupID, accountID int64) int
}

type emptyGroupQualityGateView struct{}

func (emptyGroupQualityGateView) Vetoed(int64, int64) bool { return false }

func (emptyGroupQualityGateView) OrderTier(int64, int64) int { return GroupQualityGateTierFast }

// NewEmptyGroupQualityGateView creates the safe view used until the evaluator is installed.
func NewEmptyGroupQualityGateView() GroupQualityGateView {
	return emptyGroupQualityGateView{}
}
