/**
 * Recent status values (first token, errors, cache) from an account's quality summary, formatted the same way in the
 * accounts table and the quality panel: the whole account's values over the last hour. Nothing here compares a value
 * with a threshold; the backend judges each group and model on its own (7k).
 */
import type { BackendAccountSummary, BackendDim, BackendPolicy } from '@/api/admin/schedQualityTypes'

export const RECENT_DIMS: BackendDim[] = ['ttfo', 'error', 'cache']

/** 3.2s; whole seconds from 100s up. */
export function formatRecentSeconds(ms: number): string {
  const s = ms / 1000
  return s < 100 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`
}

/** 0%, 4.2%, 12%. */
export function formatRecentPercent(ratio: number): string {
  const pct = ratio * 100
  if (pct === 0) return '0%'
  return pct < 10 ? `${pct.toFixed(1)}%` : `${Math.round(pct)}%`
}

/** One dimension of the summary: its mark and its value as text (null without a value). */
export function recentValue(quality: Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache'>, dim: BackendDim) {
  if (dim === 'ttfo') return { mark: quality.ttfo, text: quality.ttfo.p50_ms == null ? null : formatRecentSeconds(quality.ttfo.p50_ms) }
  if (dim === 'error') return { mark: quality.error, text: quality.error.rate == null ? null : formatRecentPercent(quality.error.rate) }
  return { mark: quality.cache, text: quality.cache.ratio == null ? null : `${Math.round(quality.cache.ratio * 100)}%` }
}

/**
 * The strictest threshold of each dimension among the groups that judge quality (policy enabled): the lowest first
 * token limit and error rate, the highest cache ratio. null when no group judges it. Shown as information only.
 */
export function strictestThresholds(policies: BackendPolicy[]): Record<BackendDim, number | null> {
  const enabled = policies.filter((policy) => policy.enabled)
  const pick = (values: number[], best: (...v: number[]) => number) => (values.length ? best(...values) : null)
  return {
    ttfo: pick(enabled.map((policy) => policy.ttft_seconds), Math.min),
    error: pick(enabled.map((policy) => policy.max_error_rate), Math.min),
    cache: pick(enabled.map((policy) => policy.min_cache_ratio).filter((value) => value > 0), Math.max)
  }
}

/** ≤ 10.0s, ≤ 5.0%, ≥ 90%. */
export function formatThreshold(dim: BackendDim, value: number | null): string {
  if (value == null) return '—'
  if (dim === 'ttfo') return `≤ ${value.toFixed(1)}s`
  if (dim === 'error') return `≤ ${(value * 100).toFixed(1)}%`
  return `≥ ${Math.round(value * 100)}%`
}
