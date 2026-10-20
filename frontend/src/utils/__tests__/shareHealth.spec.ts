import { describe, expect, it } from 'vitest'
import { SHARE_HEALTH_THRESHOLDS, judgeShareHealth } from '../shareHealth'
import { shareMetrics } from '@/stores/__tests__/shareSchedulingFixtures'

const levelOf = (verdict: ReturnType<typeof judgeShareHealth>, key: string) => verdict.metrics.find(m => m.key === key)?.level

describe('judgeShareHealth', () => {
  it('reports healthy when nothing moved', () => {
    const verdict = judgeShareHealth(shareMetrics(), shareMetrics())
    expect(verdict.level).toBe('ok')
    expect(verdict.metrics.map(m => m.key)).toEqual(['cacheHit', 'switchLoss', 'ttft', 'errors'])
    expect(verdict.metrics.every(m => m.level === 'ok')).toBe(true)
  })

  it('has no verdict without a baseline', () => {
    const verdict = judgeShareHealth(shareMetrics(), null)
    expect(verdict).toMatchObject({ level: 'insufficient', insufficient: 'baseline' })
    expect(verdict.metrics.every(m => m.level === 'insufficient')).toBe(true)
  })

  it('has no verdict right after a switch', () => {
    const verdict = judgeShareHealth(shareMetrics(), shareMetrics(), SHARE_HEALTH_THRESHOLDS.minWindowMinutes - 1)
    expect(verdict).toMatchObject({ level: 'insufficient', insufficient: 'window' })
    expect(judgeShareHealth(shareMetrics(), shareMetrics(), SHARE_HEALTH_THRESHOLDS.minWindowMinutes).level).toBe('ok')
  })

  it('has no verdict with too few requests on either side', () => {
    const few = SHARE_HEALTH_THRESHOLDS.minRequests - 1
    expect(judgeShareHealth(shareMetrics({ requests: few }), shareMetrics())).toMatchObject({ level: 'insufficient', insufficient: 'requests' })
    expect(judgeShareHealth(shareMetrics(), shareMetrics({ requests: few }))).toMatchObject({ level: 'insufficient', insufficient: 'requests' })
  })

  it('only asks for attention when the cache hit ratio falls', () => {
    const base = shareMetrics({ cache_hit_ratio: 0.8 })
    expect(levelOf(judgeShareHealth(shareMetrics({ cache_hit_ratio: 0.79 }), base), 'cacheHit')).toBe('ok')
    // 0.80 - 0.77 is 0.0300000…03 in floating point and counts as three points.
    expect(levelOf(judgeShareHealth(shareMetrics({ cache_hit_ratio: 0.77 }), base), 'cacheHit')).toBe('watch')
    // Time of day and model mix move it, so even a deep fall never calls for a rollback.
    const deep = judgeShareHealth(shareMetrics({ cache_hit_ratio: 0.4 }), base)
    expect(levelOf(deep, 'cacheHit')).toBe('watch')
    expect(deep.level).toBe('watch')
    // A rising ratio is never a problem.
    expect(levelOf(judgeShareHealth(shareMetrics({ cache_hit_ratio: 0.9 }), base), 'cacheHit')).toBe('ok')
  })

  it('calls for a rollback only on switch loss, errors or a slow first token', () => {
    expect(judgeShareHealth(shareMetrics({ switch_loss_ratio: 0.022 }), shareMetrics({ switch_loss_ratio: 0.002 })).level).toBe('rollback')
    expect(judgeShareHealth(shareMetrics({ error_rate: 0.04 }), shareMetrics({ error_rate: 0.02 })).level).toBe('rollback')
    expect(judgeShareHealth(shareMetrics({ ttft_p95_ms: 30000 }), shareMetrics({ ttft_p95_ms: 20000 })).level).toBe('rollback')
    // The typical first token only asks for attention, however slow.
    expect(judgeShareHealth(shareMetrics({ ttft_p50_ms: 12000 }), shareMetrics({ ttft_p50_ms: 4000 })).level).toBe('watch')
    expect(SHARE_HEALTH_THRESHOLDS.cacheHitDrop).not.toHaveProperty('rollback')
    expect(SHARE_HEALTH_THRESHOLDS.ttftP50Ratio).not.toHaveProperty('rollback')
  })

  it('grades cache lost to switching and error rate by their rise', () => {
    const base = shareMetrics({ switch_loss_ratio: 0.002, error_rate: 0.02 })
    expect(levelOf(judgeShareHealth(shareMetrics({ switch_loss_ratio: 0.007 }), base), 'switchLoss')).toBe('watch')
    expect(levelOf(judgeShareHealth(shareMetrics({ switch_loss_ratio: 0.022 }), base), 'switchLoss')).toBe('rollback')
    expect(levelOf(judgeShareHealth(shareMetrics({ error_rate: 0.025 }), base), 'errors')).toBe('watch')
    expect(levelOf(judgeShareHealth(shareMetrics({ error_rate: 0.04 }), base), 'errors')).toBe('rollback')
  })

  it('grades first token by ratio and takes the worse percentile', () => {
    const base = shareMetrics({ ttft_p50_ms: 4000, ttft_p95_ms: 20000 })
    const slowerTail = judgeShareHealth(shareMetrics({ ttft_p50_ms: 4100, ttft_p95_ms: 25000 }), base)
    expect(slowerTail.metrics.find(m => m.key === 'ttft')?.parts.map(p => p.level)).toEqual(['ok', 'watch'])
    expect(levelOf(slowerTail, 'ttft')).toBe('watch')
    const both = judgeShareHealth(shareMetrics({ ttft_p50_ms: 6000, ttft_p95_ms: 30000 }), base)
    expect(both.metrics.find(m => m.key === 'ttft')?.parts.map(p => p.level)).toEqual(['watch', 'rollback'])
    expect(levelOf(both, 'ttft')).toBe('rollback')
  })

  it('skips parts without enough samples instead of judging them', () => {
    const verdict = judgeShareHealth(
      shareMetrics({ ttft_samples: 3, ttft_p50_ms: 90000, attribution_samples: 10, switch_loss_ratio: 0.5 }),
      shareMetrics()
    )
    expect(levelOf(verdict, 'ttft')).toBe('insufficient')
    expect(levelOf(verdict, 'switchLoss')).toBe('insufficient')
    expect(verdict.level).toBe('ok')
  })

  it('ignores figures missing on either side', () => {
    const verdict = judgeShareHealth(shareMetrics({ switch_loss_ratio: null }), shareMetrics({ error_rate: null }))
    expect(levelOf(verdict, 'switchLoss')).toBe('insufficient')
    expect(levelOf(verdict, 'errors')).toBe('insufficient')
    expect(verdict.level).toBe('ok')
  })

  it('takes the worst metric for the verdict', () => {
    const verdict = judgeShareHealth(shareMetrics({ cache_hit_ratio: 0.75, error_rate: 0.05 }), shareMetrics())
    expect(levelOf(verdict, 'cacheHit')).toBe('watch')
    expect(levelOf(verdict, 'errors')).toBe('rollback')
    expect(verdict.level).toBe('rollback')
  })
})
