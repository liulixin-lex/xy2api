// Display rules shared by the quality panel, the group board and the group settings.

export type SchedDim = 'ttfo' | 'error' | 'cache'
export const SCHED_DIMS: SchedDim[] = ['ttfo', 'error', 'cache']

type Translate = (key: string, values?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

export const DASH = '—'

/** Whole seconds left until `until`, never below 0; null when there is no end time. */
export function secondsUntil(until: string | null, now: number): number | null {
  return until === null ? null : Math.max(0, Math.ceil((Date.parse(until) - now) / 1000))
}

/** Countdown `m:ss`, or `h:mm:ss` from one hour. */
export function fmtCountdown(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const rest = String(s % 60).padStart(2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${rest}` : `${m}:${rest}`
}

/**
 * The word for a value the backend sends (an identity, a place, a layer, an action, a reason …) from the copy table
 * `admin.schedQuality.<table>`; a value the table does not know shows the fallback word 其他, never a key.
 */
export function schedWord(table: string, value: string, t: Translate, te: HasKey): string {
  const key = `admin.schedQuality.${table}.${value}`
  return te(key) ? t(key) : t('admin.schedQuality.unknown')
}

/** Auto-state name; codes not in the copy table show the fallback word. */
export const kindName = (kind: string, t: Translate, te: HasKey) => schedWord('kind', kind, t, te)

/** Hard-admission reason word on the board; unknown codes show the fallback word. */
export const rejectName = (reject: string, t: Translate, te: HasKey) => schedWord('reject', reject, t, te)

/** Parent account id from a `parent_blocked:<id>` auto-state key. */
export function parentIdFromKey(key: string): number | null {
  const match = /^parent_blocked:(\d+)$/.exec(key)
  return match ? Number(match[1]) : null
}

/** Gate column of the group list: `{ttft}s · {error}% · {cache}%`. */
export function policyShort(policy: { ttft_seconds: number; max_error_rate: number; min_cache_ratio: number }, t: Translate): string {
  return t('admin.schedQuality.policy.short', {
    ttft: String(+policy.ttft_seconds.toFixed(2)),
    error: String(+(policy.max_error_rate * 100).toFixed(2)),
    cache: String(Math.round(policy.min_cache_ratio * 100))
  })
}
