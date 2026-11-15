package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/liulixin-lex/xy2api/internal/pkg/usagestats"
	"github.com/liulixin-lex/xy2api/internal/service"
)

var _ service.GatewayRequestRepository = (*usageLogRepository)(nil)

func (r *usageLogRepository) CreateUnbilledRequest(ctx context.Context, row *service.GatewayUnbilledRequest) error {
	if row == nil || row.RequestID == "" || row.CreatedAt.IsZero() || row.Result < 1 || row.Result > 4 {
		return fmt.Errorf("invalid unbilled request")
	}
	_, err := r.sql.ExecContext(ctx, `INSERT INTO gateway_unbilled_requests
		(request_id, created_at, user_id, api_key_id, group_id, account_id, platform,
		 model, stream, result, error_class, http_status, switches, fallback, takeover,
		 first_output_ms, duration_ms, input_tokens, cache_read_tokens, cache_creation_tokens, request_type)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (request_id) DO NOTHING`,
		row.RequestID, row.CreatedAt, row.UserID, row.APIKeyID, row.GroupID, row.AccountID,
		row.Platform, row.Model, row.Stream, row.Result, row.ErrorClass, row.HTTPStatus,
		row.Switches, row.Fallback, row.Takeover, row.FirstOutputMs, row.DurationMs,
		row.InputTokens, row.CacheReadTokens, row.CacheCreationTokens, row.RequestType)
	return err
}

// Both union arms have indexed group/time bounds. percentile_disc implements
// nearest-rank P50; nullable input buckets prevent incomplete usage entering the
// weighted cache denominator. Old/canceled rows still count as requests.
const groupLast60Query = `WITH requests AS (
	SELECT group_id, result, end_to_end_first_output_ms AS first_output_ms,
		input_tokens, cache_read_tokens, cache_creation_tokens
	FROM usage_logs
	WHERE group_id = ANY($1) AND created_at >= $2 AND created_at <= $3
	UNION ALL
	SELECT group_id, result, first_output_ms, input_tokens, cache_read_tokens, cache_creation_tokens
	FROM gateway_unbilled_requests
	WHERE group_id = ANY($1) AND created_at >= $2 AND created_at <= $3
)
SELECT group_id, COUNT(*),
	percentile_disc(0.5) WITHIN GROUP (ORDER BY first_output_ms) FILTER (WHERE first_output_ms IS NOT NULL),
	COUNT(*) FILTER (WHERE result = 3)::double precision / COUNT(*),
	SUM(cache_read_tokens::bigint) FILTER (WHERE input_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_creation_tokens IS NOT NULL)::double precision
	/ NULLIF(SUM(input_tokens::bigint + cache_read_tokens::bigint + cache_creation_tokens::bigint), 0)
FROM requests GROUP BY group_id`

func (r *usageLogRepository) GroupLast60(ctx context.Context, groupIDs []int64, at time.Time) (map[int64]usagestats.GroupLast60, error) {
	result := make(map[int64]usagestats.GroupLast60, len(groupIDs))
	for _, id := range groupIDs {
		result[id] = usagestats.GroupLast60{}
	}
	if len(groupIDs) == 0 {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := r.sql.QueryContext(ctx, groupLast60Query, pq.Array(groupIDs), at.Add(-time.Hour), at)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var value usagestats.GroupLast60
		if err := rows.Scan(&id, &value.Requests, &value.TTFOP50Ms, &value.FailedRate, &value.CacheRatio); err != nil {
			return nil, err
		}
		result[id] = value
	}
	return result, rows.Err()
}
