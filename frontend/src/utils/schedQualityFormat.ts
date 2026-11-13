import type {
  SchedAccountSummary, SchedGroupModelDetail, SchedGroupSummary, SchedGroupThresholds, SchedMark, SchedDim, SchedTakeoverIdleSeconds
} from '@/types/schedQuality'

// Display rules of the three scheduling pages (FINAL §6, §7): words, numbers and the two colours, red and grey.

export type { SchedDim }
export const SCHED_DIMS: SchedDim[] = ['ttfo', 'error', 'cache']

type Translate = (key: string, values?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

export const DASH = '—'

/** Error rate with one decimal. */
export const fmtErrorRate = (rate: number | null | undefined) => (rate == null ? DASH : `${(rate * 100).toFixed(1)}%`)

/** Cache ratio as a whole percent. */
export const fmtCacheRatio = (ratio: number | null | undefined) => (ratio == null ? DASH : `${Math.round(ratio * 100)}%`)

/** Countdown `m:ss`, or `h:mm:ss` from one hour. */
export function fmtCountdown(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rest = String(s % 60).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${rest}` : `${m}:${rest}`
}

/** Auto-state name; codes not in the copy table show the fallback word. */
export function kindName(kind: string, t: Translate, te: HasKey): string {
  const key = `admin.schedQuality.kind.${kind}`
  return te(key) ? t(key) : t('admin.schedQuality.unknown')
}

/** Parent account id from a `parent_blocked:<id>` auto-state key. */
export function parentIdFromKey(key: string): number | null {
  const match = /^parent_blocked:(\d+)$/.exec(key)
  return match ? Number(match[1]) : null
}

/** `HH:mm` on the day of `now`, otherwise `MM-DD HH:mm` (local time). */
export function fmtAt(iso: string, now = Date.now()): string {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`
  return d.toDateString() === new Date(now).toDateString() ? hm : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`
}

/** Milliseconds as seconds with one decimal, or a dash. */
export const fmtSeconds = (ms: number | null | undefined) => (ms == null ? DASH : `${(ms / 1000).toFixed(1)}s`)

/**
 * Colour of a number (FINAL §6): red when this dimension makes the account substandard in some group, grey when its
 * sample is too small, the default colour otherwise. Red wins over grey.
 */
export const markClass = (mark: Pick<SchedMark, 'red' | 'short'>) => (mark.red ? 'sq-red' : mark.short ? 'sq-text-3' : '')

/** Text of one account-level number: first output P50, error rate or cache ratio. */
export function markText(summary: SchedAccountSummary, dim: SchedDim): string {
  if (dim === 'ttfo') return fmtSeconds(summary.ttfo.p50_ms)
  if (dim === 'error') return fmtErrorRate(summary.error.rate)
  return fmtCacheRatio(summary.cache.ratio)
}

/** A recovery still shapes the account numbers while it is inside their 60-minute window (contract §4.11). */
export const RECOVERY_WINDOW_MS = 60 * 60_000
export const recoveredRecently = (resetAt: string | null, now: number) =>
  resetAt !== null && now - Date.parse(resetAt) < RECOVERY_WINDOW_MS

/**
 * Status column (FINAL §2): 正常, 观察中 (with 恢复于 time right after a recovery), or 停用·reason·until. The stop
 * reason uses the short word of its code; unknown codes show the fallback word.
 */
export function statusText(summary: Pick<SchedAccountSummary, 'status' | 'stop' | 'reset_at'>, now: number, t: Translate, te: HasKey): string {
  const sep = ' · '
  if (summary.status === 'disabled') {
    const parts = [t('admin.schedQuality.state.disabled')]
    if (summary.stop) {
      parts.push(kindName(summary.stop.reason, t, te))
      if (summary.stop.until) parts.push(fmtAt(summary.stop.until, now))
    }
    return parts.join('·')
  }
  if (summary.status === 'observing') {
    const word = t('admin.schedQuality.state.observing')
    return summary.reset_at && recoveredRecently(summary.reset_at, now)
      ? `${word}${sep}${t('admin.schedQuality.recover.at', { time: fmtAt(summary.reset_at, now) })}`
      : word
  }
  if (summary.status === 'normal') return t('admin.schedQuality.state.normal')
  return t('admin.schedQuality.unknown')
}

/** The three lines of a group as shown: `10s`, `5%`, `70%`. */
export function policyParts(policy: Pick<SchedGroupThresholds, 'ttft_seconds' | 'max_error_rate' | 'min_cache_ratio'>): Record<SchedDim, string> {
  return {
    ttfo: `${+policy.ttft_seconds.toFixed(2)}s`,
    error: `${+(policy.max_error_rate * 100).toFixed(2)}%`,
    cache: `${Math.round(policy.min_cache_ratio * 100)}%`
  }
}

/**
 * One dimension of a group and model in the drawer: what the verdict used and its colour. First output shows the
 * requests over the line out of n, errors the rate, cache the ratio; n is set apart for the two rates. Red = this
 * dimension fails the group's line, grey = too few samples (FINAL §6); merged evidence is marked.
 */
export function dimDetail(row: SchedGroupModelDetail, dim: SchedDim): { text: string; n: number | null; cls: string; merged: boolean } {
  const d = row[dim]
  const cls = d.verdict === 'fail' ? 'sq-red' : d.verdict === 'short' ? 'sq-text-3' : ''
  const merged = d.scope === 'merged'
  if (d.n === 0) return { text: DASH, n: null, cls: 'sq-text-3', merged: false }
  if (dim === 'ttfo') return { text: `${row.ttfo.bad}/${d.n}`, n: null, cls, merged }
  if (dim === 'error') return { text: fmtErrorRate(row.error.bad / d.n), n: d.n, cls, merged }
  return { text: fmtCacheRatio(row.cache.ratio), n: d.n, cls, merged }
}

/** 接管 as shown: 关 / 立即 / n 分钟. */
export function takeoverText(value: SchedTakeoverIdleSeconds, t: Translate): string {
  if (value === -1) return t('admin.schedQuality.policy.takeoverOff')
  if (value === 0) return t('admin.schedQuality.policy.takeoverNow')
  return t('admin.schedQuality.policy.takeoverMin', { n: value / 60 })
}

/** Scheduling columns of the group list, in order, with the header group each one sits under. */
export const GROUP_SCHED_COLUMNS = [
  ['sq_ttft', 'lines'], ['sq_error', 'lines'], ['sq_cache', 'lines'], ['sq_takeover', null],
  ['sq_normal', 'accounts'], ['sq_below', 'accounts'], ['sq_stopped', 'accounts'],
  ['sq_p50', 'last60'], ['sq_failed', 'last60'], ['sq_cache60', 'last60'], ['sq_requests', 'last60']
] as const
export type GroupSchedColumn = (typeof GROUP_SCHED_COLUMNS)[number][0]

/**
 * One cell of the group list. Lines of a group whose switch is off are grey; a substandard count above zero is red and
 * zero counts are grey; the last 60 minutes are plain numbers, a dash without requests.
 */
export function groupCell(s: SchedGroupSummary, col: GroupSchedColumn, t: Translate): { text: string; cls: string } {
  const lines = policyParts(s)
  const off = s.enabled ? '' : 'sq-text-3'
  const count = (n: number, bad = false) => ({ text: String(n), cls: n === 0 ? 'sq-text-3' : bad ? 'sq-cool' : '' })
  const recent = (text: string) => ({ text: s.last60.requests ? text : DASH, cls: s.last60.requests ? '' : 'sq-text-3' })
  switch (col) {
    case 'sq_ttft': return { text: lines.ttfo, cls: off }
    case 'sq_error': return { text: lines.error, cls: off }
    case 'sq_cache': return { text: lines.cache, cls: off }
    case 'sq_takeover': return { text: takeoverText(s.takeover_idle_seconds, t), cls: off }
    case 'sq_normal': return count(s.accounts.normal)
    case 'sq_below': return count(s.accounts.below, true)
    case 'sq_stopped': return count(s.accounts.stopped)
    case 'sq_p50': return recent(fmtSeconds(s.last60.ttfo_p50_ms))
    case 'sq_failed': return recent(fmtErrorRate(s.last60.failed_rate))
    case 'sq_cache60': return recent(fmtCacheRatio(s.last60.cache_ratio))
    case 'sq_requests': return count(s.last60.requests)
  }
}
