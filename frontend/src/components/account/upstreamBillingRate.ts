/**
 * What the upstream ratio column and the quality panel show for an API-key account's billing probe, from the row's
 * snapshot (extra.upstream_billing_probe) and the page clock. Pure: no timers, no requests.
 */
import { formatMultiplier } from '@/utils/formatters'
import type { Account, UpstreamBillingProbeSnapshot } from '@/types'

const CLOCK_SKEW_TOLERANCE_MS = 5 * 60 * 1000

const parseMinute = (value?: string) => {
  if (typeof value !== 'string') return null
  const match = /^(\d{2}):(\d{2})$/.exec(value)
  if (!match) return null
  const hour = Number(match[1])
  const minute = Number(match[2])
  return hour < 24 && minute < 60 ? hour * 60 + minute : null
}

const minuteInTimeZone = (timestamp: number, timeZone?: string) => {
  if (!timeZone) return null
  try {
    const parts = new Intl.DateTimeFormat('en-GB', { timeZone, hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(new Date(timestamp))
    const hour = Number(parts.find((part) => part.type === 'hour')?.value)
    const minute = Number(parts.find((part) => part.type === 'minute')?.value)
    return Number.isInteger(hour) && Number.isInteger(minute) ? hour * 60 + minute : null
  } catch {
    return null
  }
}

export function upstreamRateView(account: Pick<Account, 'type' | 'extra'>, now: number) {
  // Probing is open to every API-key platform (any upstream that answers /v1/sub2api/billing).
  const eligible = account.type === 'apikey'
  const snapshot: UpstreamBillingProbeSnapshot | undefined = account.extra?.upstream_billing_probe
  const data = snapshot?.data
  const probeEnabled = account.extra?.upstream_billing_probe_enabled === true
  const nextProbeAt = typeof snapshot?.next_probe_at === 'string' && Number.isFinite(Date.parse(snapshot.next_probe_at)) ? snapshot.next_probe_at : ''
  const receivedAt = typeof snapshot?.received_at === 'string' ? Date.parse(snapshot.received_at) : Number.NaN

  let freshUntil = Number.NaN
  if (typeof snapshot?.fresh_until === 'string') freshUntil = Date.parse(snapshot.fresh_until)
  else if (snapshot?.status === 'ok' && typeof snapshot.next_probe_at === 'string') {
    const next = Date.parse(snapshot.next_probe_at)
    if (Number.isFinite(next) && next > receivedAt) freshUntil = receivedAt + 2 * (next - receivedAt)
  }
  const validTimestamps = Number.isFinite(receivedAt) && receivedAt <= now + CLOCK_SKEW_TOLERANCE_MS && Number.isFinite(freshUntil) && freshUntil > receivedAt

  let stale = false
  if (snapshot) {
    if (!Number.isFinite(receivedAt)) stale = snapshot.status === 'ok'
    else stale = !validTimestamps || now > freshUntil
  }

  let currentEffectiveRate: number | null = null
  if (data && data.billing_scope === 'token') {
    const base = data.resolved_rate_multiplier
    if (typeof base === 'number' && Number.isFinite(base) && base >= 0 && typeof data.peak_rate_enabled === 'boolean') {
      if (!data.peak_rate_enabled) currentEffectiveRate = base
      else {
        const start = parseMinute(data.peak_start)
        const end = parseMinute(data.peak_end)
        const minute = minuteInTimeZone(now, data.timezone)
        const peak = data.peak_rate_multiplier
        if (start != null && end != null && minute != null && start < end && typeof peak === 'number' && Number.isFinite(peak) && peak >= 0) {
          const value = minute >= start && minute < end ? base * peak : base
          currentEffectiveRate = Number.isFinite(value) ? value : null
        }
      }
    }
  }

  const detected = data?.effective_rate_multiplier
  const lastDetectedRate = typeof detected === 'number' && Number.isFinite(detected) && detected >= 0 ? Number(detected.toPrecision(12)) : null
  const effectiveRate = !validTimestamps || stale || !['ok', 'failed'].includes(snapshot?.status ?? '') || currentEffectiveRate == null
    ? '-'
    : `${formatMultiplier(currentEffectiveRate)}x`
  const hasEffectiveRate = effectiveRate !== '-'
  // Stale: the last value stays, greyed; the tip gives the last probe time and the failure.
  const staleValue = stale && lastDetectedRate != null ? `${formatMultiplier(lastDetectedRate)}x` : ''
  const failureReason = snapshot?.status === 'failed' || stale ? snapshot?.last_error?.trim() ?? '' : ''
  const lastAttemptAt = typeof snapshot?.last_attempt_at === 'string' && Number.isFinite(Date.parse(snapshot.last_attempt_at)) ? snapshot.last_attempt_at : ''

  return {
    eligible,
    snapshot,
    data,
    probeEnabled,
    nextProbeAt,
    receivedAt,
    stale,
    currentEffectiveRate,
    lastDetectedRate,
    effectiveRate,
    hasEffectiveRate,
    staleValue,
    /** The value to show; null when there is none ("—" or a status word). */
    shownValue: hasEffectiveRate ? effectiveRate : staleValue || null,
    /** Greyed: no current value. */
    dimmed: !hasEffectiveRate,
    failureReason,
    lastAttemptAt
  }
}
