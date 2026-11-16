package schedquality

import "time"

const RuleVersion = "3.0"

type Basis struct {
	RuleVersion  string           `json:"rule_version"`
	Line         Dimension        `json:"line"`
	Kind         string           `json:"kind"` // regular | trial | review | relax
	Cut          time.Time        `json:"cut"`
	Threshold    float64          `json:"threshold"`     // 首字 L（秒）、报错上限或缓存下限
	SourceGroups []int64          `json:"source_groups"` // E2：证据来自哪些分组
	TTFT         *RuleBResult     `json:"ttft,omitempty"`
	Error        *ErrorAssessment `json:"error,omitempty"`
	Cache        *CacheAssessment `json:"cache,omitempty"`
	AttemptIDs   []string         `json:"-"` // 坏样本，最新的在前，最多 50 个，写入 sched_decisions.attempt_ids
}
type Decision struct {
	At     time.Time
	Key    LineKey
	Action Action
	Level  int
	Until  time.Time
	Basis  Basis
}
