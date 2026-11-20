import { describe, expect, it } from 'vitest'

import { formatFullLogTime, formatUsd, formatUsdTotal } from '../format'

// Mobile list plan, copy and typesetting: one request's cost has six decimals, a total two with thousands separators.
// The list's own times use the shared formatListDateTime (this year without the year); a detail always shows it.
describe('formatFullLogTime', () => {
  it('always shows the year, this year included', () => {
    expect(formatFullLogTime(new Date(new Date().getFullYear(), 10, 20, 6, 54, 11))).toBe(`${new Date().getFullYear()}-11-20 06:54:11`)
    expect(formatFullLogTime(new Date(2025, 11, 31, 23, 59, 59))).toBe('2025-12-31 23:59:59')
  })

  it('is empty for no or an invalid time', () => {
    expect(formatFullLogTime(null)).toBe('')
    expect(formatFullLogTime('not a date')).toBe('')
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
