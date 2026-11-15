package usagestats

// GroupLast60 measures delivered request experience, including requests without
// a bill. Ratios and latency are unknown when there is no matching evidence.
type GroupLast60 struct {
	TTFOP50Ms  *float64 `json:"ttfo_p50_ms"`
	FailedRate *float64 `json:"failed_rate"`
	CacheRatio *float64 `json:"cache_ratio"`
	Requests   int64    `json:"requests"`
}
