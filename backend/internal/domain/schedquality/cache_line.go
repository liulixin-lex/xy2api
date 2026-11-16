package schedquality

import "time"

const (
	CacheWindow     = 30 * time.Minute
	CacheMinTurns   = 20
	ContinuationGap = 5 * time.Minute
)

type CacheAssessment struct {
	Verdict                      Verdict  `json:"verdict"`
	MinRatio, Ratio              float64  // json: min_ratio ratio
	Turns, Sessions, LowSessions int      // json: turns sessions low_sessions；会话 = 来源分组×会话键，自身按 token 加权低于下限
	ReadTokens, InputTokens      int64    // json: read_tokens input_tokens
	AttemptIDs                   []string `json:"-"`
}

// 只取续接轮、本轮成功、token 都已知、FinishedAt∈[now-30m,now]、StartedAt>cut 的样本。
// Pass：Turns≥20 且 Ratio≥MinRatio。Fail：Turns≥20、Ratio<MinRatio 且 LowSessions≥2。其余 Unknown。没有 3 个百分点的容差。
func AssessCache(samples []AttemptSample, minRatio float64, cut, now time.Time) CacheAssessment {
	return CacheAssessment{}
}
func IsContinuation(current, previous AttemptSample) bool { return false }
