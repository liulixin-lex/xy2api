import { describe, expect, it } from 'vitest'

import { formatListDateTime } from '../format'

// Mobile list plan (copy): a list's date leaves the year out within the current one, the same digits in every language.
describe('formatListDateTime', () => {
  const now = new Date(2026, 10, 20, 9, 0, 0)
  it('leaves the year out within the current year and keeps it before', () => {
    expect(formatListDateTime(new Date(2026, 10, 20, 6, 54, 11), {}, now)).toBe('11-20 06:54:11')
    expect(formatListDateTime(new Date(2025, 0, 2, 3, 4, 5), {}, now)).toBe('2025-01-02 03:04:05')
    expect(formatListDateTime(new Date(2027, 11, 31, 23, 59, 0).getTime(), { seconds: false }, now)).toBe('2027-12-31 23:59')
    expect(formatListDateTime(new Date(2026, 11, 31, 23, 59, 0).toISOString(), { seconds: false }, now)).toBe('12-31 23:59')
  })
  it('is empty without a date or with a bad one', () => {
    expect(formatListDateTime(null, {}, now)).toBe('')
    expect(formatListDateTime('', {}, now)).toBe('')
    expect(formatListDateTime('not a date', {}, now)).toBe('')
  })
})
