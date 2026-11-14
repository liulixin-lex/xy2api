/**
 * What the upstream ratio column and the quality panel show for an API-key account's billing probe, from the row's
 * snapshot (extra.upstream_billing_probe) and the page clock. Pure: no timers, no requests. The rate follows one rule
 * (snapshotRate) whether the snapshot is fresh or stale; staleness only greys it.
 */
import { formatMultiplier } from '@/utils/formatters'
import type { Account, UpstreamBillingData, UpstreamBillingProbeSnapshot } from '@/types'

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

const isRate = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0
const tidy = (value: number) => Number(value.toPrecision(12))

/**
 * The rate a probe snapshot's data carries at `now`; null when it carries none. One rule whether the snapshot is fresh
 * or stale (7e-10):
 * - the declared rate (resolved_rate_multiplier) with the peak coefficient of the moment, when the data has the
 *   shape the backend's probe writes (token scope, a boolean peak_rate_enabled, and with peaks on a valid window,
 *   multiplier and timezone); this is the backend's own upstreamBillingRateAt, which scheduling uses;
 * - otherwise the rate the probe saw (effective_rate_multiplier), as the backend's list sort does for snapshots
 *   without the full shape.
 */
export function snapshotRate(data: Partial<UpstreamBillingData> | undefined, now: number): number | null {
  if (!data) return null
  const base = data.resolved_rate_multiplier
  if (data.billing_scope === 'token' && isRate(base) && typeof data.peak_rate_enabled === 'boolean') {
    if (!data.peak_rate_enabled) return tidy(base)
    const start = parseMinute(data.peak_start)
    const end = parseMinute(data.peak_end)
    const minute = minuteInTimeZone(now, data.timezone)
    const peak = data.peak_rate_multiplier
    if (start != null && end != null && minute != null && start < end && isRate(peak)) {
      const value = minute >= start && minute < end ? base * peak : base
      if (Number.isFinite(value)) return tidy(value)
    }
  }
  return isRate(data.effective_rate_multiplier) ? tidy(data.effective_rate_multiplier) : null
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

  // A probe that succeeded (or failed after a success, keeping its data) and carries a rate shows it: in full while
  // the snapshot is fresh, greyed once it is stale.
  const rate = snapshot && ['ok', 'failed'].includes(snapshot.status) ? snapshotRate(data, now) : null
  const current = rate != null && validTimestamps && !stale
  const rateText = rate == null ? '' : `${formatMultiplier(rate)}x`
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
    /** The rate in force now; null unless the snapshot is fresh. */
    currentEffectiveRate: current ? rate : null,
    /** The snapshot's rate, fresh or not (the stale tip names it). */
    lastDetectedRate: rate,
    effectiveRate: current ? rateText : '-',
    hasEffectiveRate: current,
    staleValue: current ? '' : rateText,
    /** The value to show; null when there is none ("—" or a status word). */
    shownValue: rateText || null,
    /** Greyed: no current value. */
    dimmed: !current,
    failureReason,
    lastAttemptAt
  }
}
