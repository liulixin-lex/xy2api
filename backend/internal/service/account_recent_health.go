package service

import (
	"context"
	"time"
)

// 账号列表“状态”列的近期健康度：近 3 分钟首字平均（usage_logs.first_token_ms，成功请求）与
// 近 10 分钟错误请求数。错误数与“使用记录 → 错误请求”页筛选该账号的口径相同，点开即能看到这些记录。

const (
	AccountRecentFirstTokenWindow = 3 * time.Minute
	AccountRecentErrorsWindow     = 10 * time.Minute
)

// AccountRecentHealth 是单个账号的近期健康度。没有首字样本时 FirstTokenAvgMs 为 nil。
type AccountRecentHealth struct {
	FirstTokenAvgMs   *int64 `json:"first_token_avg_ms"`
	FirstTokenSamples int    `json:"first_token_samples"`
	RecentErrors      int    `json:"recent_errors"`
}

// AccountRecentHealthBatchReader 是 UsageLogRepository 的可选能力（与 accountWindowStatsBatchReader
// 同一模式），不扩充 UsageLogRepository 接口，免得测试桩跟着改。导出是为了让仓储在编译期断言实现。
type AccountRecentHealthBatchReader interface {
	GetAccountRecentHealthBatch(ctx context.Context, accountIDs []int64, firstTokenSince, errorsSince time.Time) (map[int64]AccountRecentHealth, error)
}

// GetRecentHealthBatch 返回每个有效账号的近期健康度；没有记录的账号返回零值。
// 仓储不具备该能力时返回空结果，界面上只是不显示这两项。
func (s *AccountUsageService) GetRecentHealthBatch(ctx context.Context, accountIDs []int64) (map[int64]AccountRecentHealth, error) {
	ids := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	result := make(map[int64]AccountRecentHealth, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	reader, ok := s.usageLogRepo.(AccountRecentHealthBatchReader)
	if !ok {
		return result, nil
	}

	now := time.Now()
	stats, err := reader.GetAccountRecentHealthBatch(ctx, ids, now.Add(-AccountRecentFirstTokenWindow), now.Add(-AccountRecentErrorsWindow))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[id] = stats[id]
	}
	return result, nil
}
