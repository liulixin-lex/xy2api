package service

type schedulingQualityView struct {
	enabled   bool
	tiers     map[int64]int
	targets   map[int64]bool
	predicted map[int64]int
}

func (v *schedulingQualityView) OrderTier(_, a int64) int        { return v.tiers[a] }
func (v *schedulingQualityView) Enabled(int64) bool              { return v.enabled }
func (v *schedulingQualityView) MigrationTarget(_, a int64) bool { return v.targets[a] }
func (v *schedulingQualityView) ExpectedP90Ms(_, a int64) (int, bool) {
	ms, ok := v.predicted[a]
	return ms, ok
}
