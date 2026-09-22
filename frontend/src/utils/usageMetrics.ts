import type { UsageLog } from '@/types'

type MetricsRow = Pick<UsageLog, 'input_tokens' | 'cache_creation_tokens' | 'cache_read_tokens' | 'output_tokens' | 'duration_ms' | 'first_token_ms' | 'billing_mode' | 'image_count'>

function nonNegativeFinite(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

export function hasUsageTokenMetrics(row: MetricsRow): boolean {
  return !row.image_count && (!row.billing_mode || row.billing_mode === 'token')
}

export function cacheHitRate(row: MetricsRow): number | null {
  if (!hasUsageTokenMetrics(row)) return null
  const { input_tokens: input, cache_read_tokens: read, cache_creation_tokens: creation } = row
  if (![input, read, creation].every(nonNegativeFinite)) return null
  // Persisted input excludes cache reads/writes; use each bucket once.
  // These are billing log counts and may reflect forced cache billing.
  const total = input + read + creation
  return Number.isFinite(total) && total > 0 ? (read / total) * 100 : null
}

export function tokenSpeed(row: MetricsRow): number | null {
  if (!hasUsageTokenMetrics(row)) return null
  const { output_tokens: output, duration_ms: duration, first_token_ms: first } = row
  if (!nonNegativeFinite(output) || !nonNegativeFinite(duration) || duration <= 0) return null
  if (first != null && !nonNegativeFinite(first)) return null
  // Estimate from the recorded TTFT mode; output may include reasoning tokens.
  const elapsed = duration - (first ?? 0)
  if (elapsed <= 0) return null
  const speed = (output / elapsed) * 1000
  return Number.isFinite(speed) ? speed : null
}

export function formatCacheHitRate(row: MetricsRow): string {
  const rate = cacheHitRate(row)
  return rate == null ? '—' : `${rate.toFixed(1)}%`
}

export function formatTokenSpeed(row: MetricsRow): string {
  const speed = tokenSpeed(row)
  return speed == null ? '—' : `${speed.toFixed(1)} T/s`
}
