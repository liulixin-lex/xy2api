/**
 * Recent status from an account's quality summary, as the backend counts it: the whole account over the last hour
 * (from a reset within it), every model and group together, by upstream attempt. The accounts table shows three lines
 * (first token, errors, cache) and a tip card of four rows; the quality panel's recent section shows the same four.
 * Nothing here compares a value with a threshold or colours it; the group dots judge each group and model (7k).
 */
import type { BackendAccountSummary, BackendDim } from '@/api/admin/schedQualityTypes'
import { formatShortTime } from './accountTime'

export type RecentQuality = Pick<BackendAccountSummary, 'first_output' | 'slow_10s' | 'error' | 'cache' | 'recent_since'>
type Translate = (key: string, params?: Record<string, unknown>, plural?: number) => string

/** The cell's three lines. */
export const RECENT_DIMS: BackendDim[] = ['ttfo', 'error', 'cache']

/** First token, the share over 10 s and cache read muted under this many samples; errors take the backend's `short`. */
export const RECENT_MIN_SAMPLES = 20

/** 50ms under a second (0.1s would hide 50 against 100); 3.2s; whole seconds from 100s up. */
export function formatRecentSeconds(ms: number): string {
  if (Math.round(ms) < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  return s < 100 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`
}

/**
 * The same time for the 72px recent cell, at most "0.99s" / "59.9s" wide: under a second in hundredths (0.10s, 0.05s;
 * "100ms" does not fit the cell), from 1s as formatRecentSeconds but whole seconds already from 99.95s. The tip card
 * and the panel keep formatRecentSeconds.
 */
export function formatRecentSecondsCompact(ms: number): string {
  const s = ms / 1000
  if (s < 0.995) return `${s.toFixed(2)}s`
  return s < 99.95 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`
}

/** 0%, 4.2%, 12%. */
export function formatRecentPercent(ratio: number): string {
  const pct = ratio * 100
  if (pct === 0) return '0%'
  return pct < 10 ? `${pct.toFixed(1)}%` : `${Math.round(pct)}%`
}

/** Whole percent of `part` in `n`; "<1%" and ">99%" where rounding would read none or all. null for n = 0. */
export function formatSharePercent(part: number, n: number): string | null {
  if (n <= 0) return null
  const pct = Math.round((part / n) * 100)
  if (part > 0 && pct === 0) return '<1%'
  if (part < n && pct === 100) return '>99%'
  return `${pct}%`
}

/** The median first token in ms; 'timeout' when the median falls on a timeout; null without samples. */
export function firstOutputMedian({ first_output: first }: Pick<RecentQuality, 'first_output'>): number | 'timeout' | null {
  if (first.n <= 0) return null
  return first.p50_ms ?? 'timeout'
}

export type RecentKey = BackendDim | 'slow'

export interface RecentRow {
  key: RecentKey
  label: string
  /** "—" without data. */
  text: string
  /** No data or too few samples: the value reads muted. */
  muted: boolean
  /** The samples behind the value ("33 次"), ending in "样本不足" when too few; empty without a summary. */
  note: string
}

const ROW_KEYS: RecentKey[] = ['ttfo', 'slow', 'error', 'cache']

/**
 * The window, for the tip card's title and the panel's recent heading: "近 1 小时", or "HH:MM 起" when a reset within
 * the hour shortened it (the backend's recent_since).
 */
export function recentTitle(quality: Pick<RecentQuality, 'recent_since'> | null, t: Translate): string {
  return quality?.recent_since
    ? t('admin.accounts.table.tip.recentSince', { time: formatShortTime(quality.recent_since) })
    : t('admin.accounts.table.tip.recentTitle')
}

/** The four rows of the tip card and the panel: first token median, over 10 s, errors, cache over continuation turns. */
export function recentRows(quality: RecentQuality | null, t: Translate): RecentRow[] {
  const labels: Record<RecentKey, string> = {
    ttfo: t('admin.accounts.table.recent.firstOutput'),
    slow: t('admin.accounts.table.recent.slow'),
    error: t('admin.accounts.table.dims.error'),
    cache: t('admin.accounts.table.recent.cacheTurns')
  }
  if (!quality) return ROW_KEYS.map((key) => ({ key, label: labels[key], text: '—', muted: true, note: '' }))
  const row = (key: RecentKey, text: string | null, short: boolean, note: string): RecentRow => ({
    key,
    label: labels[key],
    text: text ?? '—',
    muted: short || text === null,
    note: short ? t('admin.accounts.table.tip.tooFew', { note }) : note
  })
  const { first_output: first, slow_10s: slow, error, cache } = quality
  const median = firstOutputMedian(quality)
  return [
    row(
      'ttfo',
      median === null ? null : median === 'timeout' ? t('admin.accounts.table.recent.timeout') : formatRecentSeconds(median),
      first.n < RECENT_MIN_SAMPLES,
      t('admin.accounts.table.tip.count', { n: first.n }, first.n)
    ),
    row(
      'slow',
      formatSharePercent(slow.slow, slow.n),
      slow.n < RECENT_MIN_SAMPLES,
      slow.timeouts > 0
        ? t('admin.accounts.table.tip.slowTimeouts', { n: slow.slow, timeouts: slow.timeouts })
        : t('admin.accounts.table.tip.slow', { n: slow.slow })
    ),
    row(
      'error',
      error.rate == null ? null : formatRecentPercent(error.rate),
      error.short,
      t('admin.accounts.table.tip.failed', { failed: error.failed, n: error.n }, error.n)
    ),
    row(
      'cache',
      cache.ratio == null ? null : `${Math.round(cache.ratio * 100)}%`,
      cache.turns < RECENT_MIN_SAMPLES,
      t('admin.accounts.table.tip.turns', { n: cache.turns }, cache.turns)
    )
  ]
}
