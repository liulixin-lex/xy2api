/**
 * Reads the automatic factors of a share member for display: the health
 * factor (down-weighting while an account fails more than its peers) and the
 * ramp factor (slow start after it comes back). Pure, so the rules of what
 * to show stay testable; the component turns the result into sentences.
 */

import type { ShareMember, ShareRampCause } from '@/api/admin/shareScheduling'

export interface ShareHealthDetail {
  factor: number
  rate: number | null
  baseline: number | null
  windowMinutes: number
}

export interface ShareRampDetail {
  factor: number
  cause: ShareRampCause | string | null
  /** Whole minutes since the account came back; null when unknown. */
  sinceMinutes: number | null
  /** Minutes until full weight, at least 1; null when unknown. */
  remainingMinutes: number | null
}

export interface ShareFactorView {
  /** Down-weighted for health; null when the factor is 1 or not measured. */
  health: ShareHealthDetail | null
  /** Too few samples to judge health: the account is not down-weighted. */
  insufficient: boolean
  /** Ramping up after coming back; null when at full weight. */
  ramp: ShareRampDetail | null
  /** Any automatic factor changes the weight. */
  adjusted: boolean
}

// Factors are quantised to 0.05 by the server; anything this close to 1 is 1.
const FULL = 1 - 1e-9

const minutesBetween = (from: number, to: number) => (to - from) / 60_000

export function shareFactors(member: ShareMember, now = Date.now()): ShareFactorView {
  const healthFactor = member.health_factor
  const health = healthFactor !== null && healthFactor < FULL
    ? {
        factor: healthFactor,
        rate: member.health?.rate ?? null,
        baseline: member.health?.tier_baseline_rate ?? null,
        windowMinutes: member.health?.window_minutes ?? 30
      }
    : null

  let ramp: ShareRampDetail | null = null
  if (member.ramp_factor < FULL) {
    const since = member.ramp?.since ? Date.parse(member.ramp.since) : Number.NaN
    const fullAt = member.ramp?.full_at ? Date.parse(member.ramp.full_at) : Number.NaN
    ramp = {
      factor: member.ramp_factor,
      cause: member.ramp?.cause ?? null,
      sinceMinutes: Number.isFinite(since) ? Math.max(0, Math.floor(minutesBetween(since, now))) : null,
      remainingMinutes: Number.isFinite(fullAt) ? Math.max(1, Math.ceil(minutesBetween(now, fullAt))) : null
    }
  }

  return {
    health,
    insufficient: member.health?.insufficient === true,
    ramp,
    adjusted: health !== null || ramp !== null
  }
}

/** Formats a factor as "0.2", "0.55" or "1". */
export function formatShareFactor(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '1'
  return String(Math.round(value * 100) / 100)
}

/** Formats a weight with at most one decimal: "20", "549.5". */
export function formatShareWeight(value: number): string {
  return String(Math.round(value * 10) / 10)
}
