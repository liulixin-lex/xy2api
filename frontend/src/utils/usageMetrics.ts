import type { UsageLog } from '@/types'

type TokenMetricEligibility = Partial<Pick<UsageLog,
  'billing_mode' | 'image_count' | 'image_output_tokens' | 'request_type'
>> & { video_count?: number }
type CacheTokenCounts = Pick<UsageLog, 'input_tokens' | 'cache_creation_tokens' | 'cache_read_tokens'>
type OutputRateRow = Pick<UsageLog, 'output_tokens' | 'duration_ms'>
  & Partial<Pick<UsageLog, 'first_token_ms'>> & TokenMetricEligibility

function nonNegativeFinite(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

export function hasUsageTokenMetrics(row: TokenMetricEligibility): boolean {
  return !row.image_count && !row.image_output_tokens && !row.video_count
    && (!row.billing_mode || row.billing_mode === 'token')
    && (!row.request_type || ['sync', 'stream', 'ws_v2', 'cyber'].includes(row.request_type))
}

/** Ratio of persisted billing buckets; aggregate counts before calling for a trend. */
export function cacheReadRatio(row: CacheTokenCounts): number | null {
  const { input_tokens: input, cache_read_tokens: read, cache_creation_tokens: creation } = row
  if (![input, read, creation].every(nonNegativeFinite)) return null
  // Persisted input excludes cache reads/writes; use each bucket once.
  // These are billing log counts and may reflect forced cache billing.
  const total = input + read + creation
  return Number.isFinite(total) && total > 0 ? (read / total) * 100 : null
}

export function cacheHitRate(row: CacheTokenCounts & TokenMetricEligibility): number | null {
  return hasUsageTokenMetrics(row) ? cacheReadRatio(row) : null
}

/** End-to-end rate, including first-token wait, matching upstream usage/ops. */
export function tokenSpeed(row: OutputRateRow): number | null {
  if (!hasUsageTokenMetrics(row)) return null
  const { output_tokens: output, duration_ms: duration } = row
  if (!nonNegativeFinite(output) || output <= 0 || !nonNegativeFinite(duration) || duration <= 0) return null
  // Output can already include reasoning tokens: never add them again or subtract TTFT.
  const speed = (output / duration) * 1000
  return Number.isFinite(speed) ? speed : null
}

export function formatCacheHitRate(row: CacheTokenCounts & TokenMetricEligibility): string {
  const rate = cacheHitRate(row)
  return rate == null ? '—' : `${rate.toFixed(1)}%`
}

export function formatTokenSpeed(row: OutputRateRow): string {
  const speed = tokenSpeed(row)
  return speed == null ? '—' : `${speed.toFixed(1)} tokens/s`
}
