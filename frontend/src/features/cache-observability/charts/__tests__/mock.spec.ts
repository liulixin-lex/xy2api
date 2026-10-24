import { describe, expect, it } from 'vitest'
import { mockBreakdown, mockExperiments, mockNorthStar, mockTraceStats } from '../mock'
import { resolveDataSource } from '../dataSource'

const now = new Date('2026-10-23T08:20:00Z')

function allMockPayloads() {
  return [
    mockNorthStar({ window: '7d', platform: 'all' }, now),
    mockNorthStar({ window: '30d', platform: 'all', cost_view: 'charged' }, now),
    ...(['group', 'account', 'upstream_host', 'client_family', 'protocol', 'model'] as const).map((dimension) =>
      mockBreakdown({ dimension, window: '7d', platform: 'all', sort: 'opportunity' }, now),
    ),
    mockTraceStats({ window: '24h', platform: 'openai' }, now),
    mockExperiments(now, 'running'),
  ]
}

describe('synthetic chart data', () => {
  it('carries only aggregates: no keys, HMACs, emails, user agents or production hosts', () => {
    const text = JSON.stringify(allMockPayloads())
    for (const forbidden of ['api_key', 'prompt_cache_key"', 'session_digest', 'request_id_hmac', 'conv_anchor', 'user_agent', '@']) {
      expect(text).not.toContain(forbidden)
    }
    const hosts = mockBreakdown({ dimension: 'upstream_host', window: '7d', platform: 'all', sort: 'opportunity' }, now)
    for (const row of hosts.rows) expect(row.key === 'oauth:openai' || row.key.endsWith('.example')).toBe(true)
  })

  it('keeps the north-star summary consistent with its series and amount view', () => {
    const ns = mockNorthStar({ window: '7d', platform: 'all' }, now)
    expect(ns.series).toHaveLength(168)
    expect(ns.series.reduce((s, p) => s + p.requests, 0)).toBe(ns.summary.requests)
    expect(ns.summary.token_hit_lower!).toBeLessThan(ns.summary.token_hit_upper!)
    const saved = ns.series.reduce((s, p) => s + (p.saved_amount ?? 0), 0)
    expect(saved).toBeCloseTo(ns.summary.cost!.saved_amount!, 0)
    const charged = mockNorthStar({ window: '7d', platform: 'all', cost_view: 'charged' }, now)
    expect(charged.summary.cost!.saved_amount!).toBeLessThan(ns.summary.cost!.saved_amount!)
    expect(charged.summary.cost!.notes).toContain('cost_weighted_hit_uses_list')
    expect(mockNorthStar({ window: '30d', platform: 'all' }, now).granularity).toBe('1d')
  })

  it('follows the v1.7.4 amount contract: settled-cost pricing, upstream coverage only for upstream, no fallback flag', () => {
    const list = mockNorthStar({ window: '7d', platform: 'all' }, now).summary.cost!
    expect(list).toMatchObject({ pricing_source: 'settled_cost', pricing_changed_in_window: false, upstream_covered_p_share: null, notes: [] })
    expect(list).not.toHaveProperty('pricing_snapshot_at')
    expect(list).not.toHaveProperty('pricing_mismatch_models')
    const upstream = mockNorthStar({ window: '7d', platform: 'all', cost_view: 'upstream' }, now).summary.cost!
    expect(typeof upstream.upstream_covered_p_share).toBe('number')
    expect(upstream.notes).toEqual(['cost_weighted_hit_uses_list', 'upstream_partial_coverage'])
    const models = mockBreakdown({ dimension: 'model', window: '7d', platform: 'all', sort: 'opportunity' }, now)
    expect(models.cost).toMatchObject({ currency: 'USD', view: 'list', pricing_source: 'settled_cost' })
    for (const row of models.rows) expect(row.meta?.priced === false ? row.meta.w_m === null : typeof row.meta?.w_m === 'number').toBe(true)
    expect(JSON.stringify(allMockPayloads())).not.toContain('is_fallback')
  })

  it('folds the tail into a re-weighted other row and sorts by the requested key', () => {
    const byAmount = mockBreakdown({ dimension: 'account', window: '7d', platform: 'all', sort: 'opportunity_amount', limit: 5 }, now)
    expect(byAmount.rows).toHaveLength(5)
    const amounts = byAmount.rows.map((r) => r.opportunity_amount ?? 0)
    expect([...amounts].sort((a, b) => b - a)).toEqual(amounts)
    const shares = byAmount.rows.reduce((s, r) => s + (r.opportunity_share ?? 0), 0) + (byAmount.other?.opportunity_share ?? 0)
    expect(shares).toBeCloseTo(1, 6)
  })

  it('reports trace stats only for the platforms the observation covers', () => {
    expect(mockTraceStats({ window: '1h', platform: 'openai' }, now).trace_enabled).toBe(true)
    const anthropic = mockTraceStats({ window: '1h', platform: 'anthropic' }, now)
    expect(anthropic.trace_enabled).toBe(false)
    expect(anthropic.loss_classes).toEqual([])
  })
})

describe('chart data source', () => {
  it('never serves synthetic data from a production build', () => {
    expect(resolveDataSource({ DEV: false, VITE_CACHE_PANEL_DATA_SOURCE: 'mock' }, '?source=mock')).toBe('live')
    expect(resolveDataSource({ DEV: true }, '?source=mock')).toBe('mock')
    expect(resolveDataSource({ DEV: true, VITE_CACHE_PANEL_DATA_SOURCE: 'mock' }, '')).toBe('mock')
    expect(resolveDataSource({ DEV: true }, '')).toBe('live')
  })
})
