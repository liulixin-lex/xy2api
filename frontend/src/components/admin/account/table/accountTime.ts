/**
 * Remaining time for the accounts table's countdowns, from an end time and the page's shared clock (useLiveClock).
 * Cells keep only the end time the backend sent; nothing here runs a timer.
 */
type Translate = (key: string, params?: Record<string, unknown>) => string

/** Milliseconds left until `until` (an ISO time), or null when it is missing, invalid or past. */
export function msLeft(until: string | null | undefined, now: number): number | null {
  const time = until ? Date.parse(until) : Number.NaN
  if (!Number.isFinite(time)) return null
  const left = time - now
  return left > 0 ? left : null
}

/** "2d 3h", "23h 2m", "5m" (at least 1m) with the app's countdown wording. */
export function formatRemaining(t: Translate, ms: number): string {
  const minutes = Math.max(1, Math.ceil(ms / 60_000))
  const days = Math.floor(minutes / 1440)
  const hours = Math.floor((minutes % 1440) / 60)
  const mins = minutes % 60
  if (days > 0) return t('common.time.countdown.daysHours', { d: days, h: hours })
  if (hours > 0) return t('common.time.countdown.hoursMinutes', { h: hours, m: mins })
  return t('common.time.countdown.minutes', { m: mins })
}

const pad2 = (n: number) => String(n).padStart(2, '0')

/** HH:MM for a time today, else MM-DD HH:MM (for tips): digits only, local time, whatever the interface language (12.12-9). */
export function formatShortTime(value: string | number | null | undefined): string {
  if (value == null || value === '') return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const clock = `${pad2(date.getHours())}:${pad2(date.getMinutes())}`
  return date.toDateString() === new Date().toDateString() ? clock : `${pad2(date.getMonth() + 1)}-${pad2(date.getDate())} ${clock}`
}
