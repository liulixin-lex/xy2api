package repository

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.AccountRecentHealthBatchReader = (*usageLogRepository)(nil)

// GetAccountRecentHealthBatch 为账号列表“状态”列批量统计近期首字平均与错误请求数。
//
// 两张表都是请求日志，放在用量日志仓储里，账号处理器不必再注入运维仓储。错误数复用
// “错误请求”页的 WHERE（buildOpsErrorLogsWhere，View=all），保证与该页筛选该账号后的条数一致：
// 例如只算 status>=400 的客户可见错误，不算已恢复的上游健康记录。
func (r *usageLogRepository) GetAccountRecentHealthBatch(ctx context.Context, accountIDs []int64, firstTokenSince, errorsSince time.Time) (map[int64]service.AccountRecentHealth, error) {
	result := make(map[int64]service.AccountRecentHealth, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}

	rows, err := r.sql.QueryContext(ctx, `
		SELECT account_id, AVG(first_token_ms)::float8, COUNT(first_token_ms)
		FROM usage_logs
		WHERE account_id = ANY($1) AND created_at >= $2 AND first_token_ms IS NOT NULL
		GROUP BY account_id`,
		pq.Array(accountIDs), firstTokenSince.UTC())
	if err != nil {
		return nil, fmt.Errorf("query account recent first token: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var accountID int64
		var avg float64
		var samples int
		if err := rows.Scan(&accountID, &avg, &samples); err != nil {
			return nil, fmt.Errorf("scan account recent first token: %w", err)
		}
		rounded := int64(math.Round(avg))
		health := result[accountID]
		health.FirstTokenAvgMs = &rounded
		health.FirstTokenSamples = samples
		result[accountID] = health
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query account recent first token: %w", err)
	}

	where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{View: "all", StartTime: &errorsSince})
	args = append(args, pq.Array(accountIDs))
	errorRows, err := r.sql.QueryContext(ctx,
		"SELECT e.account_id, COUNT(*) FROM ops_error_logs e "+where+
			fmt.Sprintf(" AND e.account_id = ANY($%d) GROUP BY e.account_id", len(args)),
		args...)
	if err != nil {
		return nil, fmt.Errorf("query account recent errors: %w", err)
	}
	defer func() { _ = errorRows.Close() }()
	for errorRows.Next() {
		var accountID int64
		var count int
		if err := errorRows.Scan(&accountID, &count); err != nil {
			return nil, fmt.Errorf("scan account recent errors: %w", err)
		}
		health := result[accountID]
		health.RecentErrors = count
		result[accountID] = health
	}
	if err := errorRows.Err(); err != nil {
		return nil, fmt.Errorf("query account recent errors: %w", err)
	}
	return result, nil
}
