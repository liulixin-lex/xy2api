import { describe, expect, it } from 'vitest'
import type { LossClassStat, TraceStatsResponse } from '../types'
import {
  bucketIndex,
  coverageSegments,
  formatInterval,
  formatMoney,
  formatPp,
  formatSignedMoney,
  formatTokensM,
  isStale,
  isTruncated,
  lossTable,
  runningExperiment,
  waterfallSteps,
} from '../viewModel'
import { mockExperiments } from '../mock'

const cls = (name: string, p: number, read: number, opp: number | null, excluded = false): LossClassStat => ({
  class: name,
  requests: 10,
  prompt_tokens: p,
  read_tokens: read,
  opportunity_tokens: opp,
  excluded,
})

describe('chart formatters', () => {
  it('formats intervals, deltas, tokens and money without dashes as separators', () => {
    expect(formatInterval(0.80281, 0.80322)).toBe('80.28 ~ 80.32%')
    expect(formatInterval(0.8, 0.80001)).toBe('80.00%')
    expect(formatInterval(null, null)).toBe('—')
    expect(formatPp(-1.66)).toBe('−1.66pp')
    expect(formatPp(0.004)).toBe('0.00pp')
    expect(formatTokensM(2_469_214_000)).toBe('2,469.2M')
    expect(formatMoney(18234.554, 'USD')).toBe('$18,234.55')
    expect(formatMoney(1, 'NOT-A-CODE')).toBe('1.00 NOT-A-CODE')
    expect(formatSignedMoney(-12.5, 'USD')).toBe('−$12.50')
  })
})

describe('loss waterfall', () => {
  const classes = [
    cls('under_read', 1000, 600, 300),
    cls('other_conv', 200, 100, 100),
    cls('prefix_changed', 400, 200, 150),
    cls('full', 4000, 3990, 10),
    cls('cold', 300, 50, 0),
    cls('n/a_sampled_out', 150, 100, null, true),
  ]

  it('cascades total, artifact, attributable O′ and the classes in judgment order', () => {
    const steps = waterfallSteps(classes)
    expect(steps.map((s) => s.key)).toEqual(['total', 'other_conv', 'attributable', 'full', 'prefix_changed', 'under_read'])
    expect(steps[0]).toMatchObject({ value: 560, start: 0, end: 560 })
    expect(steps[1]).toMatchObject({ value: 100, start: 460, end: 560 })
    expect(steps[2]).toMatchObject({ value: 460, start: 0, end: 460 })
    const parts = steps.filter((s) => s.kind === 'part')
    expect(parts.reduce((s, p) => s + p.value, 0)).toBe(460)
    expect(parts[parts.length - 1].start).toBe(0)
  })

  it('never attributes excluded classes and lists every class in the table', () => {
    const rows = lossTable(classes)
    expect(rows[0].cls).toBe('n/a_sampled_out')
    expect(rows.find((r) => r.cls === 'n/a_sampled_out')?.opportunityShare).toBeNull()
    expect(rows.find((r) => r.cls === 'under_read')?.opportunityShare).toBeCloseTo(300 / 560)
    expect(waterfallSteps([cls('n/a_usage', 10, 0, null, true)])).toEqual([])
  })
})

describe('coverage and freshness', () => {
  const stats = {
    window: { start: '2026-10-22T08:00:00Z', end: '2026-10-23T08:00:00Z' },
    effective_start: '2026-10-23T00:34:14Z',
    coverage: { business_prompt_tokens: 1000, traced_prompt_tokens: 950 },
    loss_classes: [cls('full', 800, 700, 5), cls('n/a_plugin', 100, 90, null, true), cls('n/a_usage', 50, 0, null, true)],
  } as unknown as TraceStatsResponse

  it('splits business prompt into judgeable, excluded and untraced', () => {
    const seg = coverageSegments(stats)!
    expect(seg.judgeable).toBeCloseTo(0.8)
    expect(seg.excluded).toBeCloseTo(0.15)
    expect(seg.untraced).toBeCloseTo(0.05)
    expect(seg.excludedBreakdown.map((e) => e.cls)).toEqual(['n/a_plugin', 'n/a_usage'])
  })

  it('flags stale data and windows cut by a restart', () => {
    const now = new Date('2026-10-23T10:30:00Z')
    expect(isStale('2026-10-23T08:00:00Z', now)).toBe(true)
    expect(isStale('2026-10-23T09:00:00Z', now)).toBe(false)
    expect(isTruncated(stats)).toBe(true)
  })

  it('places events in the bucket that contains them', () => {
    const starts = ['2026-10-23T00:00:00Z', '2026-10-23T01:00:00Z', '2026-10-23T02:00:00Z']
    expect(bucketIndex('2026-10-23T00:34:14Z', starts)).toBe(0)
    expect(bucketIndex('2026-10-23T02:59:00Z', starts)).toBe(2)
    expect(bucketIndex('2026-10-22T23:00:00Z', starts)).toBe(-1)
  })
})

describe('experiment summary', () => {
  it('picks the running experiment and its S-R slice, or nothing', () => {
    const now = new Date('2026-10-25T08:00:00Z')
    const summary = runningExperiment(mockExperiments(now, 'running').experiments)
    expect(summary?.experiment.id).toBe('EXP-1')
    expect(summary?.primary?.slice).toBe('s_r')
    expect(summary?.off?.arm).toBe('off')
    expect(runningExperiment(mockExperiments(now, 'none').experiments)).toBeNull()
  })
})
