import { describe, expect, it } from 'vitest'

import { formatFullLogTime, formatLogTime, formatUsd, formatUsdTotal } from '../format'

// Mobile list plan, copy and typesetting: this year's times drop the year; one request's cost has six decimals, a total
// two with thousands separators.
describe('formatLogTime', () => {
  const now = new Date(2026, 10, 20, 12, 0, 0)

  it('drops the year for a time in the current year', () => {
    expect(formatLogTime(new Date(2026, 10, 20, 6, 54, 11), now)).toBe('11-20 06:54:11')
    expect(formatLogTime(new Date(2026, 0, 2, 3, 4, 5), now)).toBe('01-02 03:04:05')
  })

  it('keeps the year for an earlier year, in the same digits', () => {
    expect(formatLogTime(new Date(2025, 11, 31, 23, 59, 59), now)).toBe('2025-12-31 23:59:59')
  })

  it('is empty for no or an invalid time', () => {
    expect(formatLogTime(null, now)).toBe('')
    expect(formatLogTime('not a date', now)).toBe('')
  })

  it('always shows the year in a detail view', () => {
    expect(formatFullLogTime(new Date(2026, 10, 20, 6, 54, 11))).toBe('2026-11-20 06:54:11')
  })
})

describe('formatUsd', () => {
  it('groups thousands and keeps the asked decimals', () => {
    expect(formatUsd(1234.5678901, 6)).toBe('$1,234.567890')
    expect(formatUsd(0.000237, 6)).toBe('$0.000237')
    expect(formatUsd(null, 6)).toBe('$0.000000')
  })
})

describe('formatUsdTotal', () => {
  it('shows two decimals with thousands separators', () => {
    expect(formatUsdTotal(30750.1961)).toBe('$30,750.20')
    expect(formatUsdTotal(0)).toBe('$0.00')
    expect(formatUsdTotal(undefined)).toBe('$0.00')
    expect(formatUsdTotal(0.005)).toBe('$0.01')
  })

  it('keeps six decimals for a spend that two decimals would show as none', () => {
    expect(formatUsdTotal(0.0042)).toBe('$0.004200')
    expect(formatUsdTotal(0.000001)).toBe('$0.000001')
  })
})
