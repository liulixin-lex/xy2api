import { describe, expect, it } from 'vitest'

import type { SchedBoard, SchedEvidenceNumbers, SchedGroupPolicy } from '@/types/schedQuality'
import boardFixture from '@/__tests__/fixtures/schedQuality/board.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import { cacheVerdict, errorVerdict, trialVerdicts, ttfoVerdict } from '../schedQualityVerdict'

type Ttfo = SchedEvidenceNumbers['ttfo']
type Err = SchedEvidenceNumbers['error']
type Cache = SchedEvidenceNumbers['cache']

const ttfo = (patch: Partial<Ttfo> = {}): Ttfo => ({
  n: 150, sessions: 38, p90_ms: 9200, p90_timeout: false, fail_bound_ms: 8100, fail_bound_timeout: false, ...patch
})
const err = (patch: Partial<Err> = {}): Err => ({ n: 194, sessions: 41, rate: 0.005, lcb: 0.0009, ...patch })
const cache = (patch: Partial<Cache> = {}): Cache => ({ n: 120, sessions: 30, ratio: 0.93, ...patch })

// Contract section 5, first-output row: nodata | fail: X < fail_bound | marginal: X < p90 | pass: p90 <= X.
// Clarification K1: X_ms = round(seconds * 1000), compared with whole milliseconds; *_timeout is +∞.
describe('ttfoVerdict', () => {
  it.each<[string, number, Partial<Ttfo>, string]>([
    ['nodata: 29 samples', 10, { n: 29 }, 'nodata'],
    ['nodata: 3 sessions', 10, { sessions: 3 }, 'nodata'],
    ['nodata: too few samples even when timed out', 10, { n: 12, p90_timeout: true, fail_bound_timeout: true }, 'nodata'],
    ['nodata: no p90 value', 10, { p90_ms: null, fail_bound_ms: null }, 'nodata'],
    ['pass: exactly 30 samples and 4 sessions', 10, { n: 30, sessions: 4 }, 'pass'],
    ['fail: X < fail_bound', 10, { p90_ms: 12_000, fail_bound_ms: 10_001 }, 'fail'],
    ['marginal: X = fail_bound, X < p90', 10, { p90_ms: 12_000, fail_bound_ms: 10_000 }, 'marginal'],
    ['fail: fail_bound timed out whatever the limit', 600, { p90_timeout: true, p90_ms: null, fail_bound_timeout: true, fail_bound_ms: null }, 'fail'],
    ['marginal: p90 timed out, fail_bound below X', 600, { p90_timeout: true, p90_ms: null, fail_bound_ms: 30_000 }, 'marginal'],
    ['marginal: X one millisecond below p90', 10, { p90_ms: 10_001, fail_bound_ms: 9000 }, 'marginal'],
    ['pass: X = p90', 10, { p90_ms: 10_000, fail_bound_ms: 9000 }, 'pass'],
    ['pass: fractional X = p90 (12.5 s)', 12.5, { p90_ms: 12_500, fail_bound_ms: 11_000 }, 'pass'],
    ['marginal: fractional X just below p90 (12.5 s)', 12.5, { p90_ms: 12_501, fail_bound_ms: 11_000 }, 'marginal'],
    ['fail: fractional X just below fail_bound (12.5 s)', 12.5, { p90_ms: 13_000, fail_bound_ms: 12_501 }, 'fail'],
    ['pass: X rounds up to p90 (9.9996 s → 10000 ms)', 9.9996, { p90_ms: 10_000, fail_bound_ms: 9000 }, 'pass'],
    ['marginal: X rounds down below p90 (10.0004 s → 10000 ms)', 10.0004, { p90_ms: 10_001, fail_bound_ms: 9000 }, 'marginal']
  ])('%s', (_name, seconds, patch, expected) => {
    expect(ttfoVerdict(seconds, ttfo(patch))).toBe(expected)
  })
})

// Error row: nodata | fail: Y < lcb | marginal: Y < rate | pass.
describe('errorVerdict', () => {
  it.each<[string, number, Partial<Err>, string]>([
    ['nodata: 29 samples', 0.05, { n: 29 }, 'nodata'],
    ['nodata: 3 sessions', 0.05, { sessions: 3 }, 'nodata'],
    ['nodata: no rate', 0.05, { rate: null, lcb: null }, 'nodata'],
    ['pass: exactly 30 samples and 4 sessions', 0.05, { n: 30, sessions: 4 }, 'pass'],
    ['fail: Y < lcb', 0.05, { rate: 0.12, lcb: 0.051 }, 'fail'],
    ['marginal: Y = lcb, Y < rate', 0.05, { rate: 0.08, lcb: 0.05 }, 'marginal'],
    ['marginal: lcb < Y < rate', 0.05, { rate: 0.06, lcb: 0.02 }, 'marginal'],
    ['pass: Y = rate', 0.05, { rate: 0.05, lcb: 0.02 }, 'pass'],
    ['pass: rate < Y', 0.05, { rate: 0.005, lcb: 0.0009 }, 'pass']
  ])('%s', (_name, limit, patch, expected) => {
    expect(errorVerdict(limit, err(patch))).toBe(expected)
  })
})

// Cache row: nodata | fail: ratio < Z | no marginal | pass.
describe('cacheVerdict', () => {
  it.each<[string, number, Partial<Cache>, string]>([
    ['nodata: 29 turns', 0.9, { n: 29 }, 'nodata'],
    ['nodata: 3 sessions', 0.9, { sessions: 3 }, 'nodata'],
    ['nodata: no ratio', 0.9, { ratio: null }, 'nodata'],
    ['pass: exactly 30 turns and 4 sessions', 0.9, { n: 30, sessions: 4 }, 'pass'],
    ['fail: ratio < Z', 0.9, { ratio: 0.81 }, 'fail'],
    ['pass: ratio = Z, never marginal', 0.9, { ratio: 0.9 }, 'pass'],
    ['pass: ratio > Z', 0.9, { ratio: 0.93 }, 'pass']
  ])('%s', (_name, limit, patch, expected) => {
    expect(cacheVerdict(limit, cache(patch))).toBe(expected)
  })
})

describe('trialVerdicts', () => {
  it('reproduces the board verdicts of the contract example under its saved policy', () => {
    const board = boardFixture as SchedBoard
    const { policy } = policyFixture as SchedGroupPolicy
    for (const account of board.accounts) {
      expect(trialVerdicts(policy, account)).toEqual(account.verdicts)
    }
  })

  it('follows the thresholds being edited', () => {
    const account = (boardFixture as SchedBoard).accounts[0]
    expect(trialVerdicts({ ttft_seconds: 8, max_error_rate: 0.0005, min_cache_ratio: 0.95 }, account)).toEqual({
      ttfo: 'fail',
      error: 'fail',
      cache: 'fail'
    })
  })
})
