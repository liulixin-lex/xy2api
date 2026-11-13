import type {
  SchedAccountSummary, SchedMark, SchedOutcome, SchedReason, SchedRecentAttempt, SchedState, SchedVerdict, SchedDim
} from '@/types/schedQuality'

// Display rules shared by the list, the drawer, the board and the group settings (design spec A.1, B, C, D).

export type { SchedDim }
export const SCHED_DIMS: SchedDim[] = ['ttfo', 'error', 'cache']

type Translate = (key: string, values?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

export const DASH = '—'

export const SCHED_VERDICTS: readonly SchedVerdict[] = ['pass', 'marginal', 'fail', 'nodata']
export const SCHED_STATES: readonly SchedState[] = ['normal', 'cooling']
const SCHED_OUTCOMES: readonly SchedOutcome[] = ['ok', 'timeout', 'account_fault', 'other']

/** A finite enum value from the API, or null when this UI does not know it: shown as the fallback word, neutral. */
export function known<T extends string>(value: string | null | undefined, values: readonly T[]): T | null {
  return (values as readonly string[]).includes(value ?? '') ? (value as T) : null
}

/** First output: `{x.x}s`, the timeout word, or a dash. */
export function fmtTtfo(ms: number | null | undefined, timeout: boolean, t: Translate): string {
  if (timeout) return t('admin.schedQuality.result.timeout')
  return ms == null ? DASH : `${(ms / 1000).toFixed(1)}s`
}

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

/** Hard-admission reason word on the board; unknown codes show the fallback word. */
export function rejectName(reject: string, t: Translate, te: HasKey): string {
  const key = `admin.schedQuality.reject.${reject}`
  return te(key) ? t(key) : t('admin.schedQuality.unknown')
}

/** Parent account id from a `parent_blocked:<id>` auto-state key. */
export function parentIdFromKey(key: string): number | null {
  const match = /^parent_blocked:(\d+)$/.exec(key)
  return match ? Number(match[1]) : null
}

/** Dimension a cooling reason points to. */
export function causeDim(reason: SchedReason): SchedDim | null {
  if (reason === 'speed' || reason === 'acute_timeout') return 'ttfo'
  if (reason === 'error') return 'error'
  if (reason === 'cache') return 'cache'
  return null
}

/** Result word of a recent attempt (spec B.7); null for an outcome this UI does not know. */
export function attemptResult(attempt: SchedRecentAttempt): 'ok' | 'timeout' | 'failed' | 'canceled' | null {
  if (!known(attempt.outcome, SCHED_OUTCOMES)) return null
  if (attempt.outcome === 'ok') return 'ok'
  if (attempt.outcome === 'timeout') return 'timeout'
  if (attempt.outcome === 'other' && (attempt.detail === 'client_cancel' || attempt.detail === 'unfinished')) return 'canceled'
  return 'failed'
}

/** Attempts that do not count toward any verdict are shown in the secondary colour. */
export const attemptWeak = (attempt: SchedRecentAttempt) =>
  attempt.outcome === 'other' || (attempt.outcome === 'timeout' && !(attempt.stream && attempt.warm))

/** Class of a number carrying a verdict. */
export const verdictClass = (verdict: SchedVerdict) => `sq-v-${verdict}`

/** Gate column of the group list: `{ttft}s · {error}% · {cache}%`. */
export function policyShort(policy: { ttft_seconds: number; max_error_rate: number; min_cache_ratio: number }, t: Translate): string {
  return t('admin.schedQuality.policy.short', {
    ttft: String(+policy.ttft_seconds.toFixed(2)),
    error: String(+(policy.max_error_rate * 100).toFixed(2)),
    cache: String(Math.round(policy.min_cache_ratio * 100))
  })
}

/** `HH:mm:ss` for recent attempts. */
export function fmtClock(iso: string): string {
  const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
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
