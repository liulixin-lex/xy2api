package service

import (
	"context"
	"sync"
	"time"
)

const (
	RecentStatsMaxAccounts      = 1000
	recentStatsCacheMaxAccounts = 10000
	recentStatsCutoffInterval   = 15 * time.Second
	recentStatsTTFTGoodMS       = 5000
	recentStatsTTFTMidMS        = 10000
	recentStatsTTFTMinSamples   = 5
	recentStatsErrorGood        = 0.01
	recentStatsErrorMid         = 0.03
	recentStatsErrorMinSamples  = 30
	recentStatsCacheGood        = 0.85
	recentStatsCacheMid         = 0.75
	recentStatsCacheMinSamples  = 5
)

type AccountRecentStatsRow struct {
	AccountID  int64
	TTFTP50MS  *float64
	TTFTN      int64
	CacheRatio *float64
	CacheN     int64
	ErrorN     int64
	ErrorBad   int64
}

type AccountRecentStatsRepository interface {
	// IDs are positive, unique, sorted, and bounded by the request handler.
	Batch(context.Context, []int64, time.Time, bool) ([]AccountRecentStatsRow, error)
}

type AccountRecentStatsTTFT struct {
	P50MS *float64 `json:"p50_ms"`
	N     int64    `json:"n"`
	Tier  *string  `json:"tier"`
}

type AccountRecentStatsError struct {
	Rate *float64 `json:"rate"`
	Bad  int64    `json:"bad"`
	N    int64    `json:"n"`
	Tier *string  `json:"tier"`
}

type AccountRecentStatsCache struct {
	Ratio *float64 `json:"ratio"`
	N     int64    `json:"n"`
	Tier  *string  `json:"tier"`
}

type AccountRecentStatsItem struct {
	AccountID int64                    `json:"account_id"`
	TTFT      AccountRecentStatsTTFT   `json:"ttft"`
	Error     *AccountRecentStatsError `json:"error"`
	Cache     AccountRecentStatsCache  `json:"cache"`
}

type AccountRecentStats struct {
	AsOf  time.Time                `json:"as_of"`
	Items []AccountRecentStatsItem `json:"items"`
}

type AccountRecentStatsService struct {
	repo AccountRecentStatsRepository
	ops  *OpsService
	now  func() time.Time
	mu   sync.Mutex
	// The map is one cutoff/monitoring bucket; changing either expires it.
	cacheAsOf       time.Time
	cacheMonitoring bool
	cache           map[int64]AccountRecentStatsRow
}

func NewAccountRecentStatsService(repo AccountRecentStatsRepository, ops *OpsService) *AccountRecentStatsService {
	return &AccountRecentStatsService{repo: repo, ops: ops, now: time.Now, cache: make(map[int64]AccountRecentStatsRow)}
}

// Batch accepts the positive, unique, sorted IDs validated by the handler.
func (s *AccountRecentStatsService) Batch(ctx context.Context, ids []int64) (AccountRecentStats, error) {
	monitoring := s.ops.IsMonitoringEnabled(ctx)
	asOf := s.now().UTC().Truncate(recentStatsCutoffInterval)
	out := AccountRecentStats{AsOf: asOf, Items: make([]AccountRecentStatsItem, 0, len(ids))}
	rows := make(map[int64]AccountRecentStatsRow, len(ids))
	missing := make([]int64, 0, len(ids))
	s.mu.Lock()
	if !s.cacheAsOf.Equal(asOf) || s.cacheMonitoring != monitoring {
		s.cache = make(map[int64]AccountRecentStatsRow)
		s.cacheAsOf, s.cacheMonitoring = asOf, monitoring
	}
	for _, id := range ids {
		if row, ok := s.cache[id]; ok {
			rows[id] = row
		} else {
			missing = append(missing, id)
		}
	}
	s.mu.Unlock()
	if len(missing) > 0 {
		fetched, err := s.repo.Batch(ctx, missing, asOf, monitoring)
		if err != nil {
			return out, err
		}
		for _, id := range missing {
			rows[id] = AccountRecentStatsRow{AccountID: id}
		}
		for _, row := range fetched {
			rows[row.AccountID] = row
		}
		s.mu.Lock()
		if s.cacheAsOf.Equal(asOf) && s.cacheMonitoring == monitoring {
			if len(s.cache)+len(missing) > recentStatsCacheMaxAccounts {
				clear(s.cache)
			}
			for _, id := range missing {
				s.cache[id] = rows[id]
			}
		}
		s.mu.Unlock()
	}
	for _, id := range ids {
		out.Items = append(out.Items, recentStatsItem(rows[id], monitoring))
	}
	return out, nil
}

func recentStatsItem(row AccountRecentStatsRow, monitoring bool) AccountRecentStatsItem {
	out := AccountRecentStatsItem{AccountID: row.AccountID,
		TTFT:  AccountRecentStatsTTFT{P50MS: row.TTFTP50MS, N: row.TTFTN},
		Cache: AccountRecentStatsCache{Ratio: row.CacheRatio, N: row.CacheN}}
	if row.TTFTP50MS != nil && row.TTFTN >= recentStatsTTFTMinSamples {
		out.TTFT.Tier = recentStatsTier(*row.TTFTP50MS <= recentStatsTTFTGoodMS, *row.TTFTP50MS <= recentStatsTTFTMidMS)
	}
	if row.CacheRatio != nil && row.CacheN >= recentStatsCacheMinSamples {
		out.Cache.Tier = recentStatsTier(*row.CacheRatio >= recentStatsCacheGood, *row.CacheRatio >= recentStatsCacheMid)
	}
	if monitoring {
		out.Error = &AccountRecentStatsError{N: row.ErrorN, Bad: row.ErrorBad}
		if row.ErrorN > 0 {
			rate := float64(row.ErrorBad) / float64(row.ErrorN)
			out.Error.Rate = &rate
			if row.ErrorN >= recentStatsErrorMinSamples {
				out.Error.Tier = recentStatsTier(rate <= recentStatsErrorGood, rate <= recentStatsErrorMid)
			}
		}
	}
	return out
}

func recentStatsTier(good, mid bool) *string {
	tier := "bad"
	if good {
		tier = "good"
	} else if mid {
		tier = "mid"
	}
	return &tier
}
