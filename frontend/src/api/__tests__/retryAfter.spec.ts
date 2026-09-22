import { describe, expect, it, vi } from 'vitest'
vi.mock('@/i18n', () => ({ getLocale: () => 'en' }))
import { parseRetryAfter } from '../client'
describe('Retry-After', () => {
  it('accepts seconds and HTTP dates without retrying earlier', () => {
    expect(parseRetryAfter('12')).toBe(12000)
    expect(parseRetryAfter('Tue, 22 Sep 2026 00:00:10 GMT', Date.parse('2026-09-22T00:00:00Z'))).toBe(10000)
    expect(parseRetryAfter('Tue, 22 Sep 2026 00:00:00 GMT', Date.parse('2026-09-22T00:00:10Z'))).toBe(0)
  })
  it('rejects malformed, negative, nonfinite, and overflowing values', () => {
    for (const v of ['', '-1', '1.5', 'Infinity', {}, undefined, '999999999999999999999']) expect(parseRetryAfter(v)).toBeUndefined()
  })
})
