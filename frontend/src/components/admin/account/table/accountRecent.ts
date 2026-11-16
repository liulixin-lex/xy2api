/**
 * Recent status values (first token, errors, cache) from an account's quality summary, formatted the same way in the
 * accounts table and the quality panel: the whole account's values over the last hour. First token is the first
 * useful output (what the first-output line judges; the usage records' first token is the first event), cache the
 * hit ratio of continuation turns (what the cache line judges), each with its other view beside it. Nothing here
 * compares a value with a threshold; the backend judges each group and model on its own (7k).
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

/** The cache line judges at least this many continuation turns (5.4); fewer read as too few samples. */
export const CACHE_MIN_TURNS = 20

export interface RecentValue {
  /** null without a value. */
  text: string | null
  /** Too few samples (or none). */
  short: boolean
  /** Samples behind the value: requests with a first output, requests, continuation turns. */
  n: number
}

const seconds = (ms: number | null) => (ms == null ? null : formatRecentSeconds(ms))
const percent = (ratio: number | null) => (ratio == null ? null : `${Math.round(ratio * 100)}%`)

/**
 * One line of the summary as the quality lines judge it: first token is the first useful output, cache the
 * continuation turns.
 */
export function recentValue(quality: Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache'>, dim: BackendDim): RecentValue {
  if (dim === 'ttfo') {
    const text = seconds(quality.ttfo.first_output_p50_ms)
    return { text, short: text === null, n: quality.ttfo.n }
  }
  if (dim === 'error') {
    return { text: quality.error.rate == null ? null : formatRecentPercent(quality.error.rate), short: quality.error.short, n: quality.error.n }
  }
  const text = percent(quality.cache.ratio)
  return { text, short: text === null || quality.cache.turns < CACHE_MIN_TURNS, n: quality.cache.turns }
}

/** The other views of first token and cache: the first event and its gap to the first useful output, every successful request. */
export function recentExtras(quality: Pick<BackendAccountSummary, 'ttfo' | 'cache'>) {
  return {
    firstEvent: seconds(quality.ttfo.first_event_p50_ms),
    gap: seconds(quality.ttfo.gap_p50_ms),
    cacheAll: percent(quality.cache.all_ratio),
    cacheAllRequests: quality.cache.all_requests
  }
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
