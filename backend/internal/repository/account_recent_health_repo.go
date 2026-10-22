package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.AccountRecentHealthBatchReader = (*usageLogRepository)(nil)

// GetAccountRecentHealthBatch 为账号列表“状态”列批量统计近期首字平均、错误请求数和近 30 分钟的
// 三类输入 Token 合计。
//
// 两张表都是请求日志，放在用量日志仓储里，账号处理器不必再注入运维仓储。错误数复用
// “错误请求”页的 WHERE（buildOpsErrorLogsWhere，View=all），保证与该页筛选该账号后的条数一致：
// 例如只算 status>=400 的客户可见错误，不算已恢复的上游健康记录。
func (r *usageLogRepository) GetAccountRecentHealthBatch(ctx context.Context, accountIDs []int64, firstTokenSince, errorsSince, cacheSince time.Time) (map[int64]service.AccountRecentHealth, error) {
	result := make(map[int64]service.AccountRecentHealth, len(accountIDs))
	if len(accountIDs) == 0 {
		return result, nil
	}

	// 一次扫描近 30 分钟（仍走 idx_usage_logs_account_created_at）：首字只算 firstTokenSince 之后的
	// 样本（FILTER），Token 按整段求和。窗口内没有首字样本时 AVG 为 NULL。
	rows, err := r.sql.QueryContext(ctx, `
		SELECT account_id,
			(AVG(first_token_ms) FILTER (WHERE created_at >= $3))::float8,
			COUNT(first_token_ms) FILTER (WHERE created_at >= $3),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COUNT(*)
		FROM usage_logs
		WHERE account_id = ANY($1) AND created_at >= $2
		GROUP BY account_id`,
		pq.Array(accountIDs), cacheSince.UTC(), firstTokenSince.UTC())
	if err != nil {
		return nil, fmt.Errorf("query account recent usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var accountID int64
		var avg sql.NullFloat64
		var health service.AccountRecentHealth
		if err := rows.Scan(&accountID, &avg, &health.FirstTokenSamples,
			&health.InputTokens, &health.CacheCreationTokens, &health.CacheReadTokens, &health.CacheSamples); err != nil {
			return nil, fmt.Errorf("scan account recent usage: %w", err)
		}
		if avg.Valid && health.FirstTokenSamples > 0 {
			rounded := int64(math.Round(avg.Float64))
			health.FirstTokenAvgMs = &rounded
		}
		result[accountID] = health
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query account recent usage: %w", err)
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
