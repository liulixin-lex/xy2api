import { describe, expect, it } from 'vitest'
import { formatShareFactor, formatShareWeight, shareFactors } from '../shareFactors'
import { shareMember } from '@/stores/__tests__/shareSchedulingFixtures'

const now = Date.parse('2026-10-20T12:00:00Z')

describe('shareFactors', () => {
  it('shows nothing for an account at full weight', () => {
    expect(shareFactors(shareMember(1), now)).toEqual({ health: null, insufficient: false, ramp: null, adjusted: false })
  })

  it('explains a health down-weight with the rates behind it', () => {
    const member = shareMember(1, {
      health_factor: 0.2,
      health: { window_minutes: 30, attempts: 900, soft_failures: 152, rate: 0.169, tier_baseline_rate: 0.011, insufficient: false }
    })
    expect(shareFactors(member, now)).toEqual({
      health: { factor: 0.2, rate: 0.169, baseline: 0.011, windowMinutes: 30 },
      insufficient: false,
      ramp: null,
      adjusted: true
    })
  })

  it('does not down-weight without enough samples or when the factor is not in effect', () => {
    const thin = shareMember(1, { health_factor: null, health: { window_minutes: 30, attempts: 4, soft_failures: 2, rate: 0.5, tier_baseline_rate: 0.01, insufficient: true } })
    expect(shareFactors(thin, now)).toMatchObject({ health: null, insufficient: true, adjusted: false })
    expect(shareFactors(shareMember(1, { health_factor: null, health: null }), now)).toMatchObject({ health: null, insufficient: false, adjusted: false })
  })

  it('counts the ramp from when the account came back to when it reaches full weight', () => {
    const member = shareMember(1, {
      ramp_factor: 0.4,
      ramp: { cause: 'balance_pause', since: '2026-10-20T11:56:30Z', full_at: '2026-10-20T12:06:30Z' }
    })
    expect(shareFactors(member, now).ramp).toEqual({ factor: 0.4, cause: 'balance_pause', sinceMinutes: 3, remainingMinutes: 7 })
    // Never "0 minutes left" while still ramping, and unknown times stay unknown.
    const late = shareMember(1, { ramp_factor: 0.95, ramp: { cause: 'reenabled', since: null, full_at: '2026-10-20T11:59:59Z' } })
    expect(shareFactors(late, now).ramp).toMatchObject({ sinceMinutes: null, remainingMinutes: 1 })
    const bare = shareMember(1, { ramp_factor: 0.1, ramp: null })
    expect(shareFactors(bare, now).ramp).toEqual({ factor: 0.1, cause: null, sinceMinutes: null, remainingMinutes: null })
  })

  it('reports both factors together', () => {
    const member = shareMember(1, { health_factor: 0.5, ramp_factor: 0.5, ramp: { cause: 'hard_penalty', since: null, full_at: null } })
    const view = shareFactors(member, now)
    expect(view.health?.factor).toBe(0.5)
    expect(view.ramp?.factor).toBe(0.5)
    expect(view.adjusted).toBe(true)
  })

  it('formats factors and weights compactly', () => {
    expect(formatShareFactor(0.2)).toBe('0.2')
    expect(formatShareFactor(0.55)).toBe('0.55')
    expect(formatShareFactor(0.15000000000000002)).toBe('0.15')
    expect(formatShareFactor(1)).toBe('1')
    expect(formatShareFactor(null)).toBe('1')
    expect(formatShareWeight(20)).toBe('20')
    expect(formatShareWeight(549.45)).toBe('549.5')
  })
})
