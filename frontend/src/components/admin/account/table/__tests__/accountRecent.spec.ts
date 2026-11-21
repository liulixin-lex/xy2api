import { describe, expect, it } from 'vitest'

import { recentTone, type RecentQuality } from '../accountRecent'

const quality = (patch: Partial<RecentQuality> = {}): RecentQuality => ({
  recent_since: null,
  first_output: { n: 33, timeouts: 0, p50_ms: 3000 },
  slow_10s: { n: 33, slow: 0, timeouts: 0 },
  error: { n: 50, short: false, red: false, rate: 0, failed: 0 },
  cache: { ratio: 0.9, turns: 29 },
  ...patch
})
const ttfo = (p50_ms: number | null, n = 33) => recentTone(quality({ first_output: { n, timeouts: 0, p50_ms } }), 'ttfo')
const error = (rate: number | null) => recentTone(quality({ error: { n: 50, short: false, red: false, rate, failed: 0 } }), 'error')
const cache = (ratio: number | null) => recentTone(quality({ cache: { ratio, turns: 29 } }), 'cache')

// The owner's bands (11-21), judged on the value the cell shows.
describe('recentTone', () => {
  it('first token: under 5s green, 5–10s yellow, over 10s red, a timeout red', () => {
    expect(ttfo(0)).toBe('good')
    expect(ttfo(4900)).toBe('good')
    // Shown "5.0s" (4.95s rounds up): yellow.
    expect(ttfo(4949)).toBe('good')
    expect(ttfo(4950)).toBe('warn')
    expect(ttfo(5000)).toBe('warn')
    // Shown "10.0s": still yellow; "10.1s" red.
    expect(ttfo(10_049)).toBe('warn')
    expect(ttfo(10_050)).toBe('critical')
    expect(ttfo(120_000)).toBe('critical')
    expect(recentTone(quality({ first_output: { n: 21, timeouts: 11, p50_ms: null } }), 'ttfo')).toBe('critical')
  })

  it('errors: under 1% green, 1–5% yellow (the 1–2% the owner left open included), over 5% red', () => {
    expect(error(0)).toBe('good')
    expect(error(0.009)).toBe('good')
    // Shown "0.9%" / "1.0%".
    expect(error(0.00949)).toBe('good')
    expect(error(0.00951)).toBe('warn')
    expect(error(0.01)).toBe('warn')
    expect(error(0.015)).toBe('warn')
    // Shown "5.0%" yellow, "5.1%" red.
    expect(error(0.0504)).toBe('warn')
    expect(error(0.051)).toBe('critical')
    expect(error(1)).toBe('critical')
  })

  it('cache: from 85% green, 80–85% yellow, under 80% red, in the whole percent shown', () => {
    expect(cache(1)).toBe('good')
    expect(cache(0.85)).toBe('good')
    // Shown "85%".
    expect(cache(0.845)).toBe('good')
    expect(cache(0.844)).toBe('warn')
    expect(cache(0.8)).toBe('warn')
    // Shown "80%" / "79%".
    expect(cache(0.795)).toBe('warn')
    expect(cache(0.794)).toBe('critical')
    expect(cache(0)).toBe('critical')
  })

  it('has no band without data ("—"), whatever the samples', () => {
    expect(recentTone(null, 'ttfo')).toBeNull()
    expect(ttfo(null, 0)).toBeNull()
    expect(error(null)).toBeNull()
    expect(cache(null)).toBeNull()
    // Few samples still show a value, and so a band.
    expect(ttfo(3000, 3)).toBe('good')
  })
})
