package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
)

const (
	opsRawLatencyQueryTimeout = 2 * time.Second
	opsRawPeakQueryTimeout    = 1500 * time.Millisecond
)

func (r *opsRepository) GetDashboardOverview(ctx context.Context, filter *service.OpsDashboardFilter) (*service.OpsDashboardOverview, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}

	mode := filter.QueryMode
	if !mode.IsValid() {
		mode = service.OpsQueryModeRaw
	}

	var overview *service.OpsDashboardOverview
	var err error
	switch mode {
	case service.OpsQueryModePreagg:
		overview, err = r.getDashboardOverviewPreaggregated(ctx, filter)
	case service.OpsQueryModeAuto:
		overview, err = r.getDashboardOverviewPreaggregated(ctx, filter)
		if errors.Is(err, service.ErrOpsPreaggregatedNotPopulated) {
			overview, err = r.getDashboardOverviewRaw(ctx, filter)
		}
	default:
		overview, err = r.getDashboardOverviewRaw(ctx, filter)
	}
	if err != nil {
		return nil, err
	}
	// Percentiles must use individual rates over the entire window, even in
	// preagg mode: hourly percentiles cannot be averaged into window percentiles.
	rateCtx, cancelRate := context.WithTimeout(ctx, opsRawLatencyQueryTimeout)
	defer cancelRate()
	overview.OutputTPS, err = r.queryOutputTPS(rateCtx, filter)
	if err != nil && !isQueryTimeoutErr(err) {
		return nil, err
	}
	return overview, nil
}

func (r *opsRepository) queryOutputTPS(ctx context.Context, filter *service.OpsDashboardFilter) (*service.OpsOutputTPS, error) {
	join, where, args, _ := buildUsageWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), 1)
	query := `
SELECT
 percentile_cont(0.05) WITHIN GROUP (ORDER BY output_tps),
 percentile_cont(0.10) WITHIN GROUP (ORDER BY output_tps),
 percentile_cont(0.50) WITHIN GROUP (ORDER BY output_tps),
 AVG(output_tps),
 COUNT(*)
FROM (
 SELECT ul.output_tokens * 1000.0 / NULLIF(ul.duration_ms, 0) AS output_tps
 FROM usage_logs ul
 ` + join + `
 ` + where + `
 AND ul.output_tokens > 0 AND ul.duration_ms > 0
 AND ul.image_count = 0 AND ul.image_output_tokens = 0
 AND COALESCE(ul.billing_mode, '') <> 'image'
 ` + fmt.Sprintf("AND ul.request_type IN (%d, %d, %d, %d)",
		service.RequestTypeSync, service.RequestTypeStream, service.RequestTypeWSV2, service.RequestTypeCyberBlocked) + `
) rates`
	var stats service.OpsOutputTPS
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&stats.P5, &stats.P10, &stats.P50, &stats.Avg, &stats.SampleCount); err != nil {
		// lib/pq reports query cancellation without wrapping the context error.
		// Preserve deadline vs caller cancellation for the overview's timeout policy.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return &stats, nil
}

func (r *opsRepository) getDashboardOverviewRaw(ctx context.Context, filter *service.OpsDashboardFilter) (*service.OpsDashboardOverview, error) {
	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()
	degraded := false

	successCount, tokenConsumed, err := r.queryUsageCounts(ctx, filter, start, end)
	if err != nil {
		return nil, err
	}

	latencyCtx, cancelLatency := context.WithTimeout(ctx, opsRawLatencyQueryTimeout)
	latency, err := r.queryUsageLatency(latencyCtx, filter, start, end)
	cancelLatency()
	if err != nil {
		if isQueryTimeoutErr(err) {
			degraded = true
			latency = opsUsageLatency{}
		} else {
			return nil, err
		}
	}

	errorTotal, businessLimited, errorCountSLA, upstreamExcl, upstream429, upstream529, err := r.queryErrorCounts(ctx, filter, start, end)
	if err != nil {
		return nil, err
	}

	windowSeconds := end.Sub(start).Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}

	requestCountTotal := successCount + errorTotal
	requestCountSLA := successCount + errorCountSLA

	sla := safeDivideFloat64(float64(successCount), float64(requestCountSLA))
	errorRate := safeDivideFloat64(float64(errorCountSLA), float64(requestCountSLA))
	upstreamErrorRate := safeDivideFloat64(float64(upstreamExcl), float64(requestCountSLA))

	qpsCurrent, tpsCurrent, err := r.queryCurrentRates(ctx, filter, end)
	if err != nil {
		if isQueryTimeoutErr(err) {
			degraded = true
		} else {
			return nil, err
		}
	}

	peakCtx, cancelPeak := context.WithTimeout(ctx, opsRawPeakQueryTimeout)
	qpsPeak, tpsPeak, err := r.queryPeakRates(peakCtx, filter, start, end)
	cancelPeak()
	if err != nil {
		if isQueryTimeoutErr(err) {
			degraded = true
		} else {
			return nil, err
		}
	}

	qpsAvg := roundTo1DP(float64(requestCountTotal) / windowSeconds)
	tpsAvg := roundTo1DP(float64(tokenConsumed) / windowSeconds)
	if degraded {
		if qpsCurrent <= 0 {
			qpsCurrent = qpsAvg
		}
		if tpsCurrent <= 0 {
			tpsCurrent = tpsAvg
		}
		if qpsPeak <= 0 {
			qpsPeak = roundTo1DP(math.Max(qpsCurrent, qpsAvg))
		}
		if tpsPeak <= 0 {
			tpsPeak = roundTo1DP(math.Max(tpsCurrent, tpsAvg))
		}
	}

	return &service.OpsDashboardOverview{
		StartTime: start,
		EndTime:   end,
		Platform:  strings.TrimSpace(filter.Platform),
		GroupID:   filter.GroupID,

		SuccessCount:         successCount,
		ErrorCountTotal:      errorTotal,
		BusinessLimitedCount: businessLimited,
		ErrorCountSLA:        errorCountSLA,
		RequestCountTotal:    requestCountTotal,
		RequestCountSLA:      requestCountSLA,
		TokenConsumed:        tokenConsumed,

		SLA:                          roundTo4DP(sla),
		ErrorRate:                    roundTo4DP(errorRate),
		UpstreamErrorRate:            roundTo4DP(upstreamErrorRate),
		UpstreamErrorCountExcl429529: upstreamExcl,
		Upstream429Count:             upstream429,
		Upstream529Count:             upstream529,

		QPS: service.OpsRateSummary{
			Current: qpsCurrent,
			Peak:    qpsPeak,
			Avg:     qpsAvg,
		},
		TPS: service.OpsRateSummary{
			Current: tpsCurrent,
			Peak:    tpsPeak,
			Avg:     tpsAvg,
		},

		Duration:                  latency.duration,
		TTFT:                      latency.ttft,
		E2EFirstOutput:            latency.e2e,
		E2EFirstOutputSampleCount: latency.e2eSampleCount,
	}, nil
}

type opsUsageLatency struct {
	duration        service.OpsPercentiles
	ttft            service.OpsPercentiles
	e2e             service.OpsPercentiles
	ttftSampleCount int64
	e2eSampleCount  int64
}

type opsDashboardPartial struct {
	opsUsageLatency
	successCount         int64
	errorCountTotal      int64
	businessLimitedCount int64
	errorCountSLA        int64

	upstreamErrorCountExcl429529 int64
	upstream429Count             int64
	upstream529Count             int64

	tokenConsumed int64
}

func (r *opsRepository) getDashboardOverviewPreaggregated(ctx context.Context, filter *service.OpsDashboardFilter) (*service.OpsDashboardOverview, error) {
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()

	// Stable full-hour range covered by pre-aggregation.
	aggSafeEnd := preaggSafeEnd(end)
	aggFullStart := utcCeilToHour(start)
	aggFullEnd := utcFloorToHour(aggSafeEnd)

	// If there are no stable full-hour buckets, use raw directly (short windows).
	if !aggFullStart.Before(aggFullEnd) {
		return r.getDashboardOverviewRaw(ctx, filter)
	}

	// 1) Pre-aggregated stable segment.
	preaggRows, err := r.listHourlyMetricsRows(ctx, filter, aggFullStart, aggFullEnd)
	if err != nil {
		return nil, err
	}
	if len(preaggRows) == 0 {
		// Distinguish "no data" vs "preagg not populated yet".
		if exists, err := r.rawOpsDataExists(ctx, filter, aggFullStart, aggFullEnd); err == nil && exists {
			return nil, service.ErrOpsPreaggregatedNotPopulated
		}
	}
	preagg := aggregateHourlyRows(preaggRows)

	// 2) Raw head/tail fragments (at most ~1 hour each).
	head := opsDashboardPartial{}
	tail := opsDashboardPartial{}

	if start.Before(aggFullStart) {
		part, err := r.queryRawPartial(ctx, filter, start, minTime(end, aggFullStart))
		if err != nil {
			return nil, err
		}
		head = *part
	}
	if aggFullEnd.Before(end) {
		part, err := r.queryRawPartial(ctx, filter, maxTime(start, aggFullEnd), end)
		if err != nil {
			return nil, err
		}
		tail = *part
	}

	// Merge counts.
	successCount := preagg.successCount + head.successCount + tail.successCount
	errorTotal := preagg.errorCountTotal + head.errorCountTotal + tail.errorCountTotal
	businessLimited := preagg.businessLimitedCount + head.businessLimitedCount + tail.businessLimitedCount
	errorCountSLA := preagg.errorCountSLA + head.errorCountSLA + tail.errorCountSLA

	upstreamExcl := preagg.upstreamErrorCountExcl429529 + head.upstreamErrorCountExcl429529 + tail.upstreamErrorCountExcl429529
	upstream429 := preagg.upstream429Count + head.upstream429Count + tail.upstream429Count
	upstream529 := preagg.upstream529Count + head.upstream529Count + tail.upstream529Count

	tokenConsumed := preagg.tokenConsumed + head.tokenConsumed + tail.tokenConsumed

	// Approximate percentiles across segments:
	// - p50/p90/avg: weighted average by success_count
	// - p95/p99/max: max (conservative tail)
	duration := combineApproxPercentiles([]opsPercentileSegment{
		{weight: preagg.successCount, p: preagg.duration},
		{weight: head.successCount, p: head.duration},
		{weight: tail.successCount, p: tail.duration},
	})
	// TTFT segments are weighted by the streaming sample count (rows that
	// actually recorded first_token_ms), not the total success count.
	ttft := combineApproxPercentiles([]opsPercentileSegment{
		{weight: preagg.ttftSampleCount, p: preagg.ttft},
		{weight: head.ttftSampleCount, p: head.ttft},
		{weight: tail.ttftSampleCount, p: tail.ttft},
	})
	e2e := combineApproxPercentiles([]opsPercentileSegment{
		{weight: preagg.e2eSampleCount, p: preagg.e2e},
		{weight: head.e2eSampleCount, p: head.e2e},
		{weight: tail.e2eSampleCount, p: tail.e2e},
	})

	windowSeconds := end.Sub(start).Seconds()
	if windowSeconds <= 0 {
		windowSeconds = 1
	}

	requestCountTotal := successCount + errorTotal
	requestCountSLA := successCount + errorCountSLA

	sla := safeDivideFloat64(float64(successCount), float64(requestCountSLA))
	errorRate := safeDivideFloat64(float64(errorCountSLA), float64(requestCountSLA))
	upstreamErrorRate := safeDivideFloat64(float64(upstreamExcl), float64(requestCountSLA))
	degraded := false

	// Keep "current" rates as raw, to preserve realtime semantics.
	qpsCurrent, tpsCurrent, err := r.queryCurrentRates(ctx, filter, end)
	if err != nil {
		if isQueryTimeoutErr(err) {
			degraded = true
		} else {
			return nil, err
		}
	}

	peakCtx, cancelPeak := context.WithTimeout(ctx, opsRawPeakQueryTimeout)
	qpsPeak, tpsPeak, err := r.queryPeakRates(peakCtx, filter, start, end)
	cancelPeak()
	if err != nil {
		if isQueryTimeoutErr(err) {
			degraded = true
		} else {
			return nil, err
		}
	}

	qpsAvg := roundTo1DP(float64(requestCountTotal) / windowSeconds)
	tpsAvg := roundTo1DP(float64(tokenConsumed) / windowSeconds)
	if degraded {
		if qpsCurrent <= 0 {
			qpsCurrent = qpsAvg
		}
		if tpsCurrent <= 0 {
			tpsCurrent = tpsAvg
		}
		if qpsPeak <= 0 {
			qpsPeak = roundTo1DP(math.Max(qpsCurrent, qpsAvg))
		}
		if tpsPeak <= 0 {
			tpsPeak = roundTo1DP(math.Max(tpsCurrent, tpsAvg))
		}
	}

	return &service.OpsDashboardOverview{
		StartTime: start,
		EndTime:   end,
		Platform:  strings.TrimSpace(filter.Platform),
		GroupID:   filter.GroupID,

		SuccessCount:         successCount,
		ErrorCountTotal:      errorTotal,
		BusinessLimitedCount: businessLimited,
		ErrorCountSLA:        errorCountSLA,
		RequestCountTotal:    requestCountTotal,
		RequestCountSLA:      requestCountSLA,
		TokenConsumed:        tokenConsumed,

		SLA:                          roundTo4DP(sla),
		ErrorRate:                    roundTo4DP(errorRate),
		UpstreamErrorRate:            roundTo4DP(upstreamErrorRate),
		UpstreamErrorCountExcl429529: upstreamExcl,
		Upstream429Count:             upstream429,
		Upstream529Count:             upstream529,

		QPS: service.OpsRateSummary{
			Current: qpsCurrent,
			Peak:    qpsPeak,
			Avg:     qpsAvg,
		},
		TPS: service.OpsRateSummary{
			Current: tpsCurrent,
			Peak:    tpsPeak,
			Avg:     tpsAvg,
		},

		Duration:                  duration,
		TTFT:                      ttft,
		E2EFirstOutput:            e2e,
		E2EFirstOutputSampleCount: head.e2eSampleCount + preagg.e2eSampleCount + tail.e2eSampleCount,
	}, nil
}

type opsHourlyMetricsRow struct {
	bucketStart time.Time

	successCount         int64
	ttftSampleCount      int64
	e2eSampleCount       int64
	errorCountTotal      int64
	businessLimitedCount int64
	errorCountSLA        int64

	upstreamErrorCountExcl429529 int64
	upstream429Count             int64
	upstream529Count             int64

	tokenConsumed int64

	duration hourlyPercentiles
	ttft     hourlyPercentiles
	e2e      hourlyPercentiles
}

type hourlyPercentiles struct {
	p50, p90, p95, p99, max sql.NullInt64
	avg                     sql.NullFloat64
}

func (r *opsRepository) listHourlyMetricsRows(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) ([]opsHourlyMetricsRow, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return []opsHourlyMetricsRow{}, nil
	}

	where := "bucket_start >= $1 AND bucket_start < $2"
	args := []any{start.UTC(), end.UTC()}
	idx := 3

	platform := ""
	groupID := (*int64)(nil)
	if filter != nil {
		platform = strings.TrimSpace(strings.ToLower(filter.Platform))
		groupID = filter.GroupID
	}

	switch {
	case groupID != nil && *groupID > 0:
		where += fmt.Sprintf(" AND group_id = $%d", idx)
		args = append(args, *groupID)
		idx++
		if platform != "" {
			where += fmt.Sprintf(" AND platform = $%d", idx)
			args = append(args, platform)
			// idx++ removed - not used after this
		}
	case platform != "":
		where += fmt.Sprintf(" AND platform = $%d AND group_id IS NULL", idx)
		args = append(args, platform)
		// idx++ removed - not used after this
	default:
		where += " AND platform IS NULL AND group_id IS NULL"
	}

	q := `
SELECT
  bucket_start,
  success_count,
  error_count_total,
  business_limited_count,
  error_count_sla,
  upstream_error_count_excl_429_529,
  upstream_429_count,
  upstream_529_count,
  token_consumed,
  duration_p50_ms,
  duration_p90_ms,
  duration_p95_ms,
  duration_p99_ms,
  duration_avg_ms,
  duration_max_ms,
  ttft_p50_ms,
  ttft_p90_ms,
  ttft_p95_ms,
  ttft_p99_ms,
  ttft_avg_ms,
  ttft_max_ms,
  ttft_sample_count,
  e2e_first_output_p50_ms,
  e2e_first_output_p90_ms,
  e2e_first_output_p95_ms,
  e2e_first_output_p99_ms,
  e2e_first_output_avg_ms,
  e2e_first_output_max_ms,
  e2e_first_output_sample_count
FROM ops_metrics_hourly
WHERE ` + where + `
ORDER BY bucket_start ASC`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]opsHourlyMetricsRow, 0, 64)
	for rows.Next() {
		var row opsHourlyMetricsRow
		if err := rows.Scan(
			&row.bucketStart,
			&row.successCount,
			&row.errorCountTotal,
			&row.businessLimitedCount,
			&row.errorCountSLA,
			&row.upstreamErrorCountExcl429529,
			&row.upstream429Count,
			&row.upstream529Count,
			&row.tokenConsumed,
			&row.duration.p50,
			&row.duration.p90,
			&row.duration.p95,
			&row.duration.p99,
			&row.duration.avg,
			&row.duration.max,
			&row.ttft.p50,
			&row.ttft.p90,
			&row.ttft.p95,
			&row.ttft.p99,
			&row.ttft.avg,
			&row.ttft.max,
			&row.ttftSampleCount,
			&row.e2e.p50, &row.e2e.p90, &row.e2e.p95, &row.e2e.p99, &row.e2e.avg, &row.e2e.max,
			&row.e2eSampleCount,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Keep the stored fractional averages until all buckets have been combined.
type hourlyWeightedValue struct {
	sum    float64
	weight int64
}

func (v *hourlyWeightedValue) add(value float64, valid bool, count int64) {
	if valid && count > 0 {
		v.sum += value * float64(count)
		v.weight += count
	}
}

func (v hourlyWeightedValue) rounded() *int {
	if v.weight == 0 {
		return nil
	}
	return new(int(math.Round(v.sum / float64(v.weight))))
}

type hourlyPercentileTotals struct {
	p50, p90, avg hourlyWeightedValue
	p95, p99, max *int
}

func hourlyMax(current *int, value sql.NullInt64) *int {
	if value.Valid && (current == nil || int(value.Int64) > *current) {
		return new(int(value.Int64))
	}
	return current
}

func (t *hourlyPercentileTotals) add(row hourlyPercentiles, count int64) {
	t.p50.add(float64(row.p50.Int64), row.p50.Valid, count)
	t.p90.add(float64(row.p90.Int64), row.p90.Valid, count)
	t.avg.add(row.avg.Float64, row.avg.Valid, count)
	t.p95 = hourlyMax(t.p95, row.p95)
	t.p99 = hourlyMax(t.p99, row.p99)
	t.max = hourlyMax(t.max, row.max)
}

func (t *hourlyPercentileTotals) percentiles() service.OpsPercentiles {
	return service.OpsPercentiles{P50: t.p50.rounded(), P90: t.p90.rounded(), Avg: t.avg.rounded(), P95: t.p95, P99: t.p99, Max: t.max}
}

func aggregateHourlyRows(rows []opsHourlyMetricsRow) opsDashboardPartial {
	var out opsDashboardPartial
	var duration, ttft, e2e hourlyPercentileTotals
	for _, row := range rows {
		out.successCount += row.successCount
		out.ttftSampleCount += row.ttftSampleCount
		out.e2eSampleCount += row.e2eSampleCount
		out.errorCountTotal += row.errorCountTotal
		out.businessLimitedCount += row.businessLimitedCount
		out.errorCountSLA += row.errorCountSLA

		out.upstreamErrorCountExcl429529 += row.upstreamErrorCountExcl429529
		out.upstream429Count += row.upstream429Count
		out.upstream529Count += row.upstream529Count

		out.tokenConsumed += row.tokenConsumed

		duration.add(row.duration, row.successCount)
		ttft.add(row.ttft, row.ttftSampleCount)
		e2e.add(row.e2e, row.e2eSampleCount)
	}
	out.duration, out.ttft, out.e2e = duration.percentiles(), ttft.percentiles(), e2e.percentiles()

	return out
}

func (r *opsRepository) queryRawPartial(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) (*opsDashboardPartial, error) {
	successCount, tokenConsumed, err := r.queryUsageCounts(ctx, filter, start, end)
	if err != nil {
		return nil, err
	}

	latencyCtx, cancelLatency := context.WithTimeout(ctx, opsRawLatencyQueryTimeout)
	latency, err := r.queryUsageLatency(latencyCtx, filter, start, end)
	cancelLatency()
	if err != nil {
		if isQueryTimeoutErr(err) {
			latency = opsUsageLatency{}
		} else {
			return nil, err
		}
	}

	errorTotal, businessLimited, errorCountSLA, upstreamExcl, upstream429, upstream529, err := r.queryErrorCounts(ctx, filter, start, end)
	if err != nil {
		return nil, err
	}

	return &opsDashboardPartial{
		successCount:                 successCount,
		opsUsageLatency:              latency,
		errorCountTotal:              errorTotal,
		businessLimitedCount:         businessLimited,
		errorCountSLA:                errorCountSLA,
		upstreamErrorCountExcl429529: upstreamExcl,
		upstream429Count:             upstream429,
		upstream529Count:             upstream529,
		tokenConsumed:                tokenConsumed,
	}, nil
}

func (r *opsRepository) rawOpsDataExists(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) (bool, error) {
	{
		join, where, args, _ := buildUsageWhere(filter, start, end, 1)
		q := `SELECT EXISTS(SELECT 1 FROM usage_logs ul ` + join + ` ` + where + ` LIMIT 1)`
		var exists bool
		if err := r.db.QueryRowContext(ctx, q, args...).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
	}

	{
		where, args, _ := buildErrorWhere(filter, start, end, 1)
		q := `SELECT EXISTS(SELECT 1 FROM ops_error_logs ` + where + ` LIMIT 1)`
		var exists bool
		if err := r.db.QueryRowContext(ctx, q, args...).Scan(&exists); err != nil {
			return false, err
		}
		return exists, nil
	}
}

type opsPercentileSegment struct {
	weight int64
	p      service.OpsPercentiles
}

func combineApproxPercentiles(segments []opsPercentileSegment) service.OpsPercentiles {
	weightedInt := func(get func(service.OpsPercentiles) *int) *int {
		var sum float64
		var w int64
		for _, seg := range segments {
			if seg.weight <= 0 {
				continue
			}
			v := get(seg.p)
			if v == nil {
				continue
			}
			sum += float64(*v) * float64(seg.weight)
			w += seg.weight
		}
		if w <= 0 {
			return nil
		}
		out := int(math.Round(sum / float64(w)))
		return &out
	}

	maxInt := func(get func(service.OpsPercentiles) *int) *int {
		var max *int
		for _, seg := range segments {
			v := get(seg.p)
			if v == nil {
				continue
			}
			if max == nil || *v > *max {
				c := *v
				max = &c
			}
		}
		return max
	}

	return service.OpsPercentiles{
		P50: weightedInt(func(p service.OpsPercentiles) *int { return p.P50 }),
		P90: weightedInt(func(p service.OpsPercentiles) *int { return p.P90 }),
		P95: maxInt(func(p service.OpsPercentiles) *int { return p.P95 }),
		P99: maxInt(func(p service.OpsPercentiles) *int { return p.P99 }),
		Avg: weightedInt(func(p service.OpsPercentiles) *int { return p.Avg }),
		Max: maxInt(func(p service.OpsPercentiles) *int { return p.Max }),
	}
}

func preaggSafeEnd(endTime time.Time) time.Time {
	now := time.Now().UTC()
	cutoff := now.Add(-5 * time.Minute)
	if endTime.After(cutoff) {
		return cutoff
	}
	return endTime
}

func utcCeilToHour(t time.Time) time.Time {
	u := t.UTC()
	f := u.Truncate(time.Hour)
	if f.Equal(u) {
		return f
	}
	return f.Add(time.Hour)
}

func utcFloorToHour(t time.Time) time.Time {
	return t.UTC().Truncate(time.Hour)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (r *opsRepository) queryUsageCounts(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) (successCount int64, tokenConsumed int64, err error) {
	join, where, args, _ := buildUsageWhere(filter, start, end, 1)

	q := `
SELECT
  COALESCE(COUNT(*), 0) AS success_count,
  COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS token_consumed
FROM usage_logs ul
` + join + `
` + where

	var tokens sql.NullInt64
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&successCount, &tokens); err != nil {
		return 0, 0, err
	}
	if tokens.Valid {
		tokenConsumed = tokens.Int64
	}
	return successCount, tokenConsumed, nil
}

func (r *opsRepository) queryUsageLatency(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) (opsUsageLatency, error) {
	join, where, args, _ := buildUsageWhere(filter, start, end, 1)
	q := `
SELECT
  percentile_cont(0.50) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p50,
  percentile_cont(0.90) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p90,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p95,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_p99,
  AVG(duration_ms) FILTER (WHERE duration_ms IS NOT NULL) AS duration_avg,
  MAX(duration_ms) AS duration_max,
  percentile_cont(0.50) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE first_token_ms IS NOT NULL) AS ttft_p50,
  percentile_cont(0.90) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE first_token_ms IS NOT NULL) AS ttft_p90,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE first_token_ms IS NOT NULL) AS ttft_p95,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE first_token_ms IS NOT NULL) AS ttft_p99,
  AVG(first_token_ms) FILTER (WHERE first_token_ms IS NOT NULL) AS ttft_avg,
  MAX(first_token_ms) AS ttft_max,
  COUNT(first_token_ms) AS ttft_sample_count,
  percentile_cont(0.50) WITHIN GROUP (ORDER BY end_to_end_first_output_ms) FILTER (WHERE end_to_end_first_output_ms IS NOT NULL) AS e2e_first_output_p50,
  percentile_cont(0.90) WITHIN GROUP (ORDER BY end_to_end_first_output_ms) FILTER (WHERE end_to_end_first_output_ms IS NOT NULL) AS e2e_first_output_p90,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY end_to_end_first_output_ms) FILTER (WHERE end_to_end_first_output_ms IS NOT NULL) AS e2e_first_output_p95,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY end_to_end_first_output_ms) FILTER (WHERE end_to_end_first_output_ms IS NOT NULL) AS e2e_first_output_p99,
  AVG(end_to_end_first_output_ms) FILTER (WHERE end_to_end_first_output_ms IS NOT NULL) AS e2e_first_output_avg,
  MAX(end_to_end_first_output_ms) AS e2e_first_output_max,
  COUNT(end_to_end_first_output_ms) AS e2e_first_output_sample_count
FROM usage_logs ul
` + join + `
` + where

	var duration, ttft, e2e rawLatencyPercentiles
	var out opsUsageLatency
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(
		&duration.p50, &duration.p90, &duration.p95, &duration.p99, &duration.avg, &duration.max,
		&ttft.p50, &ttft.p90, &ttft.p95, &ttft.p99, &ttft.avg, &ttft.max, &out.ttftSampleCount,
		&e2e.p50, &e2e.p90, &e2e.p95, &e2e.p99, &e2e.avg, &e2e.max, &out.e2eSampleCount,
	); err != nil {
		return opsUsageLatency{}, err
	}
	out.duration, out.ttft, out.e2e = duration.percentiles(), ttft.percentiles(), e2e.percentiles()
	return out, nil
}

type rawLatencyPercentiles struct {
	p50, p90, p95, p99, avg sql.NullFloat64
	max                     *int
}

func (p rawLatencyPercentiles) percentiles() service.OpsPercentiles {
	return service.OpsPercentiles{
		P50: floatToIntPtr(p.p50), P90: floatToIntPtr(p.p90), P95: floatToIntPtr(p.p95),
		P99: floatToIntPtr(p.p99), Avg: floatToIntPtr(p.avg), Max: p.max,
	}
}

func (r *opsRepository) queryErrorCounts(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) (
	errorTotal int64,
	businessLimited int64,
	errorCountSLA int64,
	upstreamExcl429529 int64,
	upstream429 int64,
	upstream529 int64,
	err error,
) {
	where, args, _ := buildErrorWhere(filter, start, end, 1)

	q := `
SELECT
  COALESCE(COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400), 0) AS error_total,
  COALESCE(COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400 AND is_business_limited), 0) AS business_limited,
  COALESCE(COUNT(*) FILTER (WHERE COALESCE(status_code, 0) >= 400 AND NOT is_business_limited), 0) AS error_sla,
  COALESCE(COUNT(*) FILTER (WHERE error_owner = 'provider' AND NOT is_business_limited AND COALESCE(upstream_status_code, status_code, 0) NOT IN (429, 529)), 0) AS upstream_excl,
  COALESCE(COUNT(*) FILTER (WHERE error_owner = 'provider' AND NOT is_business_limited AND COALESCE(upstream_status_code, status_code, 0) = 429), 0) AS upstream_429,
  COALESCE(COUNT(*) FILTER (WHERE error_owner = 'provider' AND NOT is_business_limited AND COALESCE(upstream_status_code, status_code, 0) = 529), 0) AS upstream_529
FROM ops_error_logs
` + where

	if err := r.db.QueryRowContext(ctx, q, args...).Scan(
		&errorTotal,
		&businessLimited,
		&errorCountSLA,
		&upstreamExcl429529,
		&upstream429,
		&upstream529,
	); err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	return errorTotal, businessLimited, errorCountSLA, upstreamExcl429529, upstream429, upstream529, nil
}

func (r *opsRepository) queryCurrentRates(ctx context.Context, filter *service.OpsDashboardFilter, end time.Time) (qpsCurrent float64, tpsCurrent float64, err error) {
	windowStart := end.Add(-1 * time.Minute)

	successCount1m, token1m, err := r.queryUsageCounts(ctx, filter, windowStart, end)
	if err != nil {
		return 0, 0, err
	}
	errorCount1m, _, _, _, _, _, err := r.queryErrorCounts(ctx, filter, windowStart, end)
	if err != nil {
		return 0, 0, err
	}

	qpsCurrent = roundTo1DP(float64(successCount1m+errorCount1m) / 60.0)
	tpsCurrent = roundTo1DP(float64(token1m) / 60.0)
	return qpsCurrent, tpsCurrent, nil
}

func (r *opsRepository) queryPeakRates(ctx context.Context, filter *service.OpsDashboardFilter, start, end time.Time) (qpsPeak float64, tpsPeak float64, err error) {
	usageJoin, usageWhere, usageArgs, next := buildUsageWhere(filter, start, end, 1)
	errorWhere, errorArgs, _ := buildErrorWhere(filter, start, end, next)

	q := `
WITH usage_buckets AS (
  SELECT
    date_trunc('minute', ul.created_at) AS bucket,
    COUNT(*) AS req_cnt,
    COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS token_cnt
  FROM usage_logs ul
  ` + usageJoin + `
  ` + usageWhere + `
  GROUP BY 1
),
error_buckets AS (
  SELECT date_trunc('minute', created_at) AS bucket, COUNT(*) AS err_cnt
  FROM ops_error_logs
  ` + errorWhere + `
    AND COALESCE(status_code, 0) >= 400
  GROUP BY 1
),
combined AS (
  SELECT COALESCE(u.bucket, e.bucket) AS bucket,
         COALESCE(u.req_cnt, 0) + COALESCE(e.err_cnt, 0) AS total_req,
         COALESCE(u.token_cnt, 0) AS total_tokens
  FROM usage_buckets u
  FULL OUTER JOIN error_buckets e ON u.bucket = e.bucket
)
SELECT
  COALESCE(MAX(total_req), 0) AS max_req_per_min,
  COALESCE(MAX(total_tokens), 0) AS max_tokens_per_min
FROM combined`

	args := append(usageArgs, errorArgs...)

	var maxReqPerMinute, maxTokensPerMinute sql.NullInt64
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&maxReqPerMinute, &maxTokensPerMinute); err != nil {
		return 0, 0, err
	}
	if maxReqPerMinute.Valid && maxReqPerMinute.Int64 > 0 {
		qpsPeak = roundTo1DP(float64(maxReqPerMinute.Int64) / 60.0)
	}
	if maxTokensPerMinute.Valid && maxTokensPerMinute.Int64 > 0 {
		tpsPeak = roundTo1DP(float64(maxTokensPerMinute.Int64) / 60.0)
	}
	return qpsPeak, tpsPeak, nil
}

func isQueryTimeoutErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

func buildUsageWhere(filter *service.OpsDashboardFilter, start, end time.Time, startIndex int) (join string, where string, args []any, nextIndex int) {
	platform := ""
	groupID := (*int64)(nil)
	if filter != nil {
		platform = strings.TrimSpace(strings.ToLower(filter.Platform))
		groupID = filter.GroupID
	}

	idx := startIndex
	clauses := make([]string, 0, 4)
	args = make([]any, 0, 4)

	args = append(args, start)
	clauses = append(clauses, fmt.Sprintf("ul.created_at >= $%d", idx))
	idx++
	args = append(args, end)
	clauses = append(clauses, fmt.Sprintf("ul.created_at < $%d", idx))
	idx++

	if groupID != nil && *groupID > 0 {
		args = append(args, *groupID)
		clauses = append(clauses, fmt.Sprintf("ul.group_id = $%d", idx))
		idx++
	}
	if platform != "" {
		// Prefer group.platform when available; fall back to account.platform so we don't
		// drop rows where group_id is NULL.
		join = "LEFT JOIN groups g ON g.id = ul.group_id LEFT JOIN accounts a ON a.id = ul.account_id"
		args = append(args, platform)
		clauses = append(clauses, fmt.Sprintf("COALESCE(NULLIF(g.platform,''), a.platform) = $%d", idx))
		idx++
	}

	where = "WHERE " + strings.Join(clauses, " AND ")
	return join, where, args, idx
}

func buildErrorWhere(filter *service.OpsDashboardFilter, start, end time.Time, startIndex int) (where string, args []any, nextIndex int) {
	platform := ""
	groupID := (*int64)(nil)
	if filter != nil {
		platform = strings.TrimSpace(strings.ToLower(filter.Platform))
		groupID = filter.GroupID
	}

	idx := startIndex
	clauses := make([]string, 0, 5)
	args = make([]any, 0, 5)

	args = append(args, start)
	clauses = append(clauses, fmt.Sprintf("created_at >= $%d", idx))
	idx++
	args = append(args, end)
	clauses = append(clauses, fmt.Sprintf("created_at < $%d", idx))
	idx++

	clauses = append(clauses, "is_count_tokens = FALSE")

	if groupID != nil && *groupID > 0 {
		args = append(args, *groupID)
		clauses = append(clauses, fmt.Sprintf("group_id = $%d", idx))
		idx++
	}
	if platform != "" {
		args = append(args, platform)
		clauses = append(clauses, fmt.Sprintf("platform = $%d", idx))
		idx++
	}

	where = "WHERE " + strings.Join(clauses, " AND ")
	return where, args, idx
}

func floatToIntPtr(v sql.NullFloat64) *int {
	if !v.Valid {
		return nil
	}
	n := int(math.Round(v.Float64))
	return &n
}

func safeDivideFloat64(numerator float64, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}

func roundTo1DP(v float64) float64 {
	return math.Round(v*10) / 10
}

func roundTo4DP(v float64) float64 {
	return math.Round(v*10000) / 10000
}
