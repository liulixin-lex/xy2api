import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true,
  }),
}))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn() } }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {} } }))
vi.mock('vue-chartjs', () => ({ Line: { props: ['data', 'options', 'plugins'], template: '<canvas data-test="line-chart" />' } }))

import ChartsPanel from '../ChartsPanel.vue'
import { createMockCachePanelClient, mockTraceStats } from '../mock'
import { useCacheCharts } from '../useCacheCharts'
import type { CachePanelClient } from '../types'

const now = () => new Date('2026-10-23T08:20:00Z')
const stubs = { Icon: true, HelpTooltip: true }

function setup(overrides: Partial<CachePanelClient> = {}, experiments: 'running' | 'none' = 'running') {
  const base = createMockCachePanelClient({ now, latencyMs: 0, experiments })
  const client: CachePanelClient = {
    getNorthStar: vi.fn(base.getNorthStar),
    getBreakdown: vi.fn(base.getBreakdown),
    getTraceStats: vi.fn(base.getTraceStats),
    listExperiments: vi.fn(base.listExperiments),
    ...overrides,
  }
  // Group 101 is some group's availability fallback, so the ranking tags it (CACHE_PANEL_API §7.1).
  const loadGroups = vi.fn(async () => [{ id: 5, availability_fallback_group_id: 101 }])
  const state = useCacheCharts({ client, source: 'live', loadGroups })
  const wrapper = mount(ChartsPanel, { props: { state }, global: { stubs } })
  return { wrapper, state, client }
}

describe('useCacheCharts', () => {
  beforeEach(() => vi.clearAllMocks())

  it('treats a 404 from northstar as "charts not in this build" and stops there', async () => {
    const { state, client } = setup({ getNorthStar: vi.fn().mockRejectedValue({ status: 404 }) })
    await state.loadAll()
    expect(state.available.value).toBe(false)
    expect(client.getBreakdown).not.toHaveBeenCalled()
    expect(client.getTraceStats).not.toHaveBeenCalled()
  })

  it('isolates failures per section and maps 503 to "rollup not ready"', async () => {
    const { state } = setup({
      getBreakdown: vi.fn().mockRejectedValue({ status: 503, code: 'CACHE_ROLLUP_NOT_READY' }),
      listExperiments: vi.fn().mockRejectedValue({ status: 500, message: 'boom' }),
    })
    await state.loadAll()
    expect(state.available.value).toBe(true)
    expect(state.northstar.status).toBe('ready')
    expect(state.breakdown.status).toBe('not_ready')
    expect(state.experiments).toMatchObject({ status: 'error', error: 'boom' })
    expect(state.stats.status).toBe('ready')
  })

  it('keeps the previous data on screen while refetching', async () => {
    const { state } = setup()
    await state.loadAll()
    const before = state.northstar.data
    const pending = state.loadNorthStar()
    expect(state.northstar.refreshing).toBe(true)
    expect(state.northstar.status).toBe('ready')
    expect(state.northstar.data).toBe(before)
    await pending
    expect(state.northstar.refreshing).toBe(false)
  })
})

describe('ChartsPanel', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renders the daily check: hero range, deploy-aware trend, waterfall, coverage and the experiment strip', async () => {
    const { wrapper, state } = setup()
    await state.loadAll()
    await flushPromises()
    expect(wrapper.get('[data-test="northstar-hero"]').text()).toMatch(/%$/)
    expect(wrapper.find('[data-test="line-chart"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="northstar-cost"]').text()).toContain('admin.cacheCharts.cost.saved')
    const steps = wrapper.findAll('[data-test="waterfall"] [data-step]').map((n) => n.attributes('data-step'))
    expect(steps.slice(0, 3)).toEqual(['total', 'other_conv', 'attributable'])
    expect(steps).toContain('under_read')
    expect(wrapper.find('[data-test="experiment-strip"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="breakdown-table"]').exists()).toBe(true)
  })

  it('asks for the switches when cache_trace is off, and hides the strip without a running experiment', async () => {
    const { wrapper, state } = setup(
      { getTraceStats: vi.fn(async () => ({ ...mockTraceStats({ window: '24h', platform: 'openai' }, now()), trace_enabled: false })) },
      'none',
    )
    await state.loadAll()
    await flushPromises()
    const disabled = wrapper.findAll('[data-test="trace-disabled"]')
    expect(disabled.length).toBeGreaterThan(0)
    await disabled[0].get('button').trigger('click')
    expect(wrapper.emitted('open-settings')).toHaveLength(1)
    expect(wrapper.find('[data-test="experiment-strip"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="experiments-empty"]').exists()).toBe(true)
  })

  it('tags fallback groups from the admin group list and shows the settled-cost basis and notes', async () => {
    const { wrapper, state } = setup()
    await state.loadAll()
    await flushPromises()
    const rows = wrapper.findAll('[data-test="breakdown-table"] tbody tr')
    expect(rows.find((r) => r.text().includes('codex-pro'))!.text()).toContain('admin.cacheCharts.breakdown.fallback')
    expect(rows.find((r) => r.text().includes('codex-team'))!.text()).not.toContain('admin.cacheCharts.breakdown.fallback')
    expect(wrapper.get('[data-test="cost-basis"]').text()).toContain('admin.cacheCharts.cost.basis')
    state.setFilter('costView', 'upstream')
    // The filter watcher runs after this tick, then the synthetic client answers on a timer.
    await flushPromises()
    await flushPromises()
    const notes = wrapper.get('[data-test="cost-notes"]').text()
    expect(notes).toContain('admin.cacheCharts.cost.notes.cost_weighted_hit_uses_list')
    expect(notes).toContain('admin.cacheCharts.cost.notes.upstream_partial_coverage')
    expect(wrapper.get('[data-test="cost-basis"]').text()).toContain('admin.cacheCharts.cost.upstreamCovered')
  })

  it('explains missing settled cost and refetches the ranking for another dimension', async () => {
    const { wrapper, state, client } = setup()
    const base = createMockCachePanelClient({ now, latencyMs: 0 })
    vi.mocked(client.getNorthStar).mockImplementation(async (q) => {
      const ns = await base.getNorthStar(q)
      return { ...ns, summary: { ...ns.summary, cost: null } }
    })
    await state.loadAll()
    await flushPromises()
    expect(wrapper.get('[data-test="northstar-cost"]').text()).toContain('admin.cacheCharts.cost.unavailable')
    await wrapper.get('[data-test="dimension-model"]').trigger('click')
    await flushPromises()
    expect(client.getBreakdown).toHaveBeenLastCalledWith(expect.objectContaining({ dimension: 'model', cost_view: 'list' }))
  })
})

describe('chart copy', () => {
  it('keeps the chart locale trees symmetric and free of em and en dashes', () => {
    const keys = (v: unknown, p = ''): string[] =>
      v && typeof v === 'object' ? Object.entries(v).flatMap(([k, c]) => keys(c, p ? `${p}.${k}` : k)) : [p]
    const pick = (l: typeof zh) => (l.admin as Record<string, unknown>).cacheCharts
    expect(keys(pick(zh))).toEqual(keys(pick(en)))
    expect(JSON.stringify(pick(zh)) + JSON.stringify(pick(en))).not.toMatch(/[—–]/)
  })
})
