import { describe, expect, it } from 'vitest'
import { cacheHitRate, tokenSpeed, formatCacheHitRate, formatTokenSpeed } from '../usageMetrics'

const row = { input_tokens: 500, cache_read_tokens: 1500, cache_creation_tokens: 0, output_tokens: 100, duration_ms: 3000, first_token_ms: 1000, image_count: 0, billing_mode: 'token' }

describe('per-request usage metrics', () => {
  it('uses all prompt tokens and formats one decimal', () => {
    expect(cacheHitRate(row)).toBe(75)
    expect(formatCacheHitRate(row)).toBe('75.0%')
    expect(formatCacheHitRate({ ...row, input_tokens: 200, cache_creation_tokens: 300, cache_read_tokens: 500 })).toBe('50.0%')
    expect(formatCacheHitRate({ ...row, input_tokens: 2, cache_read_tokens: 1 })).toBe('33.3%')
    expect(formatCacheHitRate({ ...row, cache_read_tokens: 0 })).toBe('0.0%')
  })
  it('keeps interleaved sessions and users independent even after reordering', () => {
    const requests = [
      { ...row, user_id: 1, session_id: 'a' },
      { ...row, user_id: 1, session_id: 'b', cache_read_tokens: 0 },
      { ...row, user_id: 2, session_id: 'a', input_tokens: 1500 },
      { ...row, user_id: 1, session_id: 'a', input_tokens: 1000 },
    ]
    expect(requests.map(formatCacheHitRate)).toEqual(['75.0%', '0.0%', '50.0%', '60.0%'])
    expect(requests.reverse().map(formatCacheHitRate)).toEqual(['60.0%', '50.0%', '0.0%', '75.0%'])
  })
  it('calculates generation speed and falls back only when first-token time is absent', () => {
    expect(tokenSpeed(row)).toBe(50)
    expect(formatTokenSpeed(row)).toBe('50.0 T/s')
    expect(formatTokenSpeed({ ...row, first_token_ms: null })).toBe('33.3 T/s')
    expect(formatTokenSpeed({ ...row, first_token_ms: 0 })).toBe('33.3 T/s')
    expect(formatTokenSpeed({ ...row, output_tokens: 0 })).toBe('0.0 T/s')
  })
  it.each([0, -1, null, NaN, Infinity])('handles invalid duration %s', duration_ms => {
    expect(formatTokenSpeed({ ...row, duration_ms })).toBe('—')
  })
  it.each([-1, 3000, 4000, NaN, Infinity])('handles invalid first-token time %s', first_token_ms => {
    expect(formatTokenSpeed({ ...row, first_token_ms })).toBe('—')
  })
  it.each([-1, NaN, Infinity])('handles invalid token count %s', value => {
    expect(formatTokenSpeed({ ...row, output_tokens: value })).toBe('—')
    for (const key of ['input_tokens', 'cache_read_tokens', 'cache_creation_tokens']) {
      expect(formatCacheHitRate({ ...row, [key]: value })).toBe('—')
    }
  })
  it('handles empty input and inapplicable media billing', () => {
    expect(formatCacheHitRate({ ...row, input_tokens: 0, cache_read_tokens: 0 })).toBe('—')
    for (const media of [{ image_count: 1 }, { billing_mode: 'image' }, { billing_mode: 'video' }, { billing_mode: 'per_request' }]) {
      expect(formatCacheHitRate({ ...row, ...media })).toBe('—')
      expect(formatTokenSpeed({ ...row, ...media })).toBe('—')
    }
  })
})
