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

/** hh:mm for a time today, else MM-DD hh:mm (for tips). */
export function formatShortTime(value: string | number | null | undefined): string {
  if (value == null || value === '') return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const sameDay = date.toDateString() === new Date().toDateString()
  const options: Intl.DateTimeFormatOptions = sameDay
    ? { hour: '2-digit', minute: '2-digit', hour12: false }
    : { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
  return new Intl.DateTimeFormat(undefined, options).format(date)
}
