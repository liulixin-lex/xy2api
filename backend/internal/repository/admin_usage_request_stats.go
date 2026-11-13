package repository

import (
	"context"
	"fmt"
	"time"
)

func (r *usageLogRepository) AdminRequestStats(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	where, args, err := adminRequestWhere(filters)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	query := fmt.Sprintf(adminRequestsCTE+`
		, scoped AS (
			SELECT
				COALESCE(NULLIF(TRIM(inbound_endpoint), ''), 'unknown') AS inbound_endpoint,
				COALESCE(NULLIF(TRIM(upstream_endpoint), ''), 'unknown') AS upstream_endpoint,
				input_tokens,
				output_tokens,
				cache_creation_tokens,
				cache_read_tokens,
				total_cost,
				actual_cost,
				COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1) AS account_cost,
				duration_ms, result, end_to_end_first_output_ms
			FROM requests
			%s
		)
		SELECT
			GROUPING(inbound_endpoint) AS inbound_grouped,
			GROUPING(upstream_endpoint) AS upstream_grouped,
			inbound_endpoint,
			upstream_endpoint,
			COUNT(*) AS requests,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
			COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
			COALESCE(SUM(total_cost), 0) AS cost,
			COALESCE(SUM(actual_cost), 0) AS actual_cost,
			COALESCE(SUM(account_cost), 0) AS account_cost,
			COALESCE(AVG(duration_ms), 0) AS avg_duration_ms,
            COUNT(*) FILTER (WHERE result=1), COUNT(*) FILTER (WHERE result=2), COUNT(*) FILTER (WHERE result=3),
            percentile_disc(0.5) WITHIN GROUP (ORDER BY end_to_end_first_output_ms) FILTER (WHERE end_to_end_first_output_ms IS NOT NULL),
            SUM(cache_read_tokens::bigint) FILTER (WHERE input_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_creation_tokens IS NOT NULL)::double precision
            / NULLIF(SUM(input_tokens::bigint+cache_read_tokens::bigint+cache_creation_tokens::bigint),0)
		FROM scoped
		GROUP BY GROUPING SETS (
			(),
			(inbound_endpoint),
			(upstream_endpoint),
			(inbound_endpoint, upstream_endpoint)
		)
	`, where)

	return r.readUsageStats(ctx, query, args, filters, true)
}
