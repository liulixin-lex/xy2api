import { describe, expect, it } from 'vitest'

import {
  formatCompactMoney,
  formatCompactRequests,
  formatCompactTokens,
  formatExactCount,
  formatExactMoney
} from '../compactNumber'

describe('compact numbers', () => {
  it('writes money with two decimals below $1,000 and three significant digits from there', () => {
    expect(formatCompactMoney(0)).toBe('$0.00')
    expect(formatCompactMoney(999.99)).toBe('$999.99')
    expect(formatCompactMoney(1234)).toBe('$1.23K')
    expect(formatCompactMoney(12_345.67)).toBe('$12.3K')
    expect(formatCompactMoney(123_456)).toBe('$123K')
    expect(formatCompactMoney(1_234_567)).toBe('$1.23M')
    expect(formatCompactMoney(12_345_678)).toBe('$12.3M')
  })

  it('rounds before it picks the unit', () => {
    expect(formatCompactMoney(999.995)).toBe('$1.00K')
    expect(formatCompactMoney(9999.99)).toBe('$10.0K')
    expect(formatCompactMoney(99_999.99)).toBe('$100K')
    expect(formatCompactMoney(999_999.99)).toBe('$1.00M')
    expect(formatCompactRequests(999_950)).toBe('1.00M')
    expect(formatCompactTokens(999_500)).toBe('1.00M')
  })

  it('rounds a midpoint up, binary fractions included', () => {
    expect(formatCompactMoney(9995)).toBe('$10.0K')
    expect(formatCompactMoney(9994.99)).toBe('$9.99K')
    expect(formatCompactMoney(1235)).toBe('$1.24K')
    expect(formatCompactMoney(0.005)).toBe('$0.01')
    expect(formatCompactTokens(1235)).toBe('1.24K')
    expect(formatCompactRequests(123_500)).toBe('124K')
  })

  it('keeps requests whole below 100,000', () => {
    expect(formatCompactRequests(99_999)).toBe('99,999')
    expect(formatCompactRequests(100_000)).toBe('100K')
    expect(formatCompactRequests(128_456)).toBe('128K')
    expect(formatCompactRequests(1_234_567)).toBe('1.23M')
  })

  it('writes tokens on the same three-digit rule', () => {
    expect(formatCompactTokens(532)).toBe('532')
    expect(formatCompactTokens(96_123_456)).toBe('96.1M')
    expect(formatCompactTokens(532_730_000)).toBe('533M')
    expect(formatCompactTokens(1_200_000)).toBe('1.20M')
  })

  it('gives the exact value for the hover tip', () => {
    expect(formatExactMoney(1_234_567.891)).toBe('$1,234,567.89')
    expect(formatExactCount(532_730_000)).toBe('532,730,000')
  })
})
