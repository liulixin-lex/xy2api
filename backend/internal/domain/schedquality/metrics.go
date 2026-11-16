package schedquality

import "time"

type Dimension string

const (
	DimensionTTFO  Dimension = "ttfo"
	DimensionError Dimension = "error"
	DimensionCache Dimension = "cache"
)

type AccountSummary struct {
	From, To                                       time.Time
	FirstEventP50Ms, FirstOutputP50Ms, GapP50Ms    *int     // 流式且两种时间都有的样本；Gap 是逐条差值的中位数
	ErrorN, ErrorBad, CacheTurns, CacheAllRequests int      // 报错按请求单位、口径同报错线（不填补）
	CacheRatio, CacheAllRatio                      *float64 // 判定口径（续接轮）、全部成功请求口径
}

func SummarizeAccount(samples []AttemptSample, accountID int64, reset, now time.Time) AccountSummary {
	return AccountSummary{}
}
