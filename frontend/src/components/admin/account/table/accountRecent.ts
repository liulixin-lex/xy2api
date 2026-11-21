/**
 * Recent status from an account's quality summary, as the backend counts it: the whole account over the last hour
 * (from a reset within it), every model and group together, by upstream attempt. The accounts table shows three lines
 * (first token, errors, cache) and a tip card of four rows; the quality panel's recent section shows the same four.
 * The table's three values take a colour from the owner's bands (recentTone); the group dots still judge each group
 * and model (7k).
 */
import type { BackendAccountSummary, BackendDim } from '@/api/admin/schedQualityTypes'
import type { LatencySeverity } from '@/utils/latencyHealth'

export type RecentQuality = Pick<BackendAccountSummary, 'first_output' | 'slow_10s' | 'error' | 'cache' | 'recent_since'>
type Translate = (key: string, params?: Record<string, unknown>, plural?: number) => string

/** The cell's three lines. */
export const RECENT_DIMS: BackendDim[] = ['ttfo', 'error', 'cache']

/** First token, the share over 10 s and cache read muted under this many samples; errors take the backend's `short`. */
export const RECENT_MIN_SAMPLES = 20

/** 50ms under a second (0.1s would hide 50 against 100); 3.2s; whole seconds from 100s up. The group board's format. */
export function formatRecentSeconds(ms: number): string {
  if (Math.round(ms) < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  return s < 100 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`
}

/**
 * The recent status's first token, the same in the 72px cell, its tip card and the panel, at most "0.99s" / "59.9s"
 * wide: under a second in hundredths (0.10s, 0.05s; "100ms" does not fit the cell), then tenths, whole seconds from
 * 99.95s (120s).
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

/** Cache in whole percent: 93. */
const cachePercent = (ratio: number) => Math.round(ratio * 100)

export type RecentTone = Extract<LatencySeverity, 'good' | 'warn' | 'critical'>

/**
 * The owner's colour bands for the table's three values (11-21), on the value as shown, so "5.0s" is yellow: first
 * token under 5s green, 5–10s yellow, over 10s red; errors under 1% green, 1–5% yellow, over 5% red (the owner left
 * 1–2% open; it takes yellow); cache from 85% green, 80–85% yellow, under 80% red.
 */
export const RECENT_BANDS = {
  ttfo: { goodBelow: 5, warnUpTo: 10 },
  error: { goodBelow: 1, warnUpTo: 5 },
  cache: { goodFrom: 85, warnFrom: 80 }
} as const

const lowIsGood = (shown: number, band: { goodBelow: number; warnUpTo: number }): RecentTone =>
  shown < band.goodBelow ? 'good' : shown <= band.warnUpTo ? 'warn' : 'critical'

/**
 * A value's band, from the number the cell shows ("3.9s", "4.2%", "93%"); a median on a timeout is red. null without
 * data ("—"), which stays muted.
 */
export function recentTone(quality: RecentQuality | null, dim: BackendDim): RecentTone | null {
  if (!quality) return null
  if (dim === 'ttfo') {
    const median = firstOutputMedian(quality)
    if (median === null) return null
    return median === 'timeout' ? 'critical' : lowIsGood(parseFloat(formatRecentSecondsCompact(median)), RECENT_BANDS.ttfo)
  }
  if (dim === 'error') {
    const rate = quality.error.rate
    return rate == null ? null : lowIsGood(parseFloat(formatRecentPercent(rate)), RECENT_BANDS.error)
  }
  const ratio = quality.cache.ratio
  if (ratio == null) return null
  const shown = cachePercent(ratio)
  return shown >= RECENT_BANDS.cache.goodFrom ? 'good' : shown >= RECENT_BANDS.cache.warnFrom ? 'warn' : 'critical'
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

const pad2 = (n: number) => String(n).padStart(2, '0')

/**
 * The window, for the tip card's title and the panel's recent heading: "近 1 小时", or "HH:MM 起" in local time when a
 * reset within the hour shortened it (the backend's recent_since). Never a date: the window is an hour at most, so
 * "23:55 起" just after midnight is still clear.
 */
export function recentTitle(quality: Pick<RecentQuality, 'recent_since'> | null, t: Translate): string {
  const since = quality?.recent_since ? new Date(quality.recent_since) : null
  return since && !Number.isNaN(since.getTime())
    ? t('admin.accounts.table.tip.recentSince', { time: `${pad2(since.getHours())}:${pad2(since.getMinutes())}` })
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
      median === null ? null : median === 'timeout' ? t('admin.accounts.table.recent.timeout') : formatRecentSecondsCompact(median),
      first.n < RECENT_MIN_SAMPLES,
      t('admin.accounts.table.tip.count', { n: first.n }, first.n)
    ),
    row(
      'slow',
      formatSharePercent(slow.slow, slow.n),
      slow.n < RECENT_MIN_SAMPLES,
      slow.timeouts > 0
        // n is the timeouts: English takes its plural from it ("1 timeout").
        ? t('admin.accounts.table.tip.slowTimeouts', { slow: slow.slow, n: slow.timeouts }, slow.timeouts)
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
      cache.ratio == null ? null : `${cachePercent(cache.ratio)}%`,
      cache.turns < RECENT_MIN_SAMPLES,
      t('admin.accounts.table.tip.turns', { n: cache.turns }, cache.turns)
    )
  ]
}
