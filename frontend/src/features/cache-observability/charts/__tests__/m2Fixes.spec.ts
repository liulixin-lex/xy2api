// R's M2-U-3 findings (09-review-p0a/REVIEW_M2.md), fixed after M2: each test fails on the code before
// the fix. The panel runs on the synthetic client, as in ChartsPanel.spec; the view test drives the
// live client through a mocked apiClient so the availability probe is the production one.
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const chart = vi.hoisted(() => ({ options: null as null | { scales: { x: { title?: { display: boolean; text: string } } } } }))
const api = vi.hoisted(() => ({ get: vi.fn() }))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true,
  }),
}))
vi.mock('@/api/client', () => ({ apiClient: { get: api.get } }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getAllIncludingInactive: vi.fn(async () => []) } } }))
vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options', 'plugins'],
    setup(props: { options: typeof chart.options }) {
      chart.options = props.options
      return {}
    },
    template: '<canvas data-test="line-chart" />',
  },
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn() }), onBeforeRouteLeave: vi.fn() }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('../../settings/CacheSettingsPanel.vue', () => ({ default: { template: '<div data-test="settings-stub" />' } }))

import CacheObservabilityView from '../../CacheObservabilityView.vue'
import ChartsPanel from '../ChartsPanel.vue'
import { createMockCachePanelClient, mockNorthStar } from '../mock'
import type { BreakdownResponse, CachePanelClient } from '../types'
import { useCacheCharts } from '../useCacheCharts'
import { EMPTY, formatCount, formatInterval, formatMoney, formatPct, formatPp, formatSignedMoney, formatTokensM } from '../viewModel'

const now = () => new Date('2026-10-23T08:20:00Z')
const stubs = { Icon: true, HelpTooltip: true }

function setup(overrides: Partial<CachePanelClient> = {}) {
  const base = createMockCachePanelClient({ now, latencyMs: 0, experiments: 'running' })
  const client: CachePanelClient = {
    getNorthStar: vi.fn(base.getNorthStar),
    getBreakdown: vi.fn(base.getBreakdown),
    getTraceStats: vi.fn(base.getTraceStats),
    listExperiments: vi.fn(base.listExperiments),
    ...overrides,
  }
  const state = useCacheCharts({ client, source: 'live', loadGroups: vi.fn(async () => []) })
  const wrapper = mount(ChartsPanel, { props: { state }, global: { stubs } })
  return { wrapper, state, client, base }
}

describe("R's M2-U-3 findings, fixed", () => {
  beforeEach(() => {
    vi.clearAllMocks()
    chart.options = null
  })

  it('shows a missing value as the em dash of contract R5', () => {
    expect(EMPTY).toBe('—')
    for (const text of [formatPct(null), formatInterval(null, null), formatPp(null), formatTokensM(null), formatCount(null), formatMoney(null, 'USD'), formatSignedMoney(null, 'USD')]) {
      expect(text).toBe('—')
    }
  })

  it('keeps no old freshness or experiment strip after a failed read', async () => {
    const { wrapper, state, client } = setup()
    await state.loadAll()
    await flushPromises()
    expect(wrapper.get('[data-test="freshness"]').text()).toContain('admin.cacheCharts.freshness')
    expect(wrapper.find('[data-test="experiment-strip"]').exists()).toBe(true)

    vi.mocked(client.getNorthStar).mockRejectedValue({ status: 500, message: 'boom' })
    vi.mocked(client.listExperiments).mockRejectedValue({ status: 500, message: 'boom' })
    await state.loadAll()
    await flushPromises()
    expect(state.northstar.status).toBe('error')
    expect(state.experiments.status).toBe('error')
    expect(wrapper.get('[data-test="freshness"]').text()).not.toContain('admin.cacheCharts.freshness')
    expect(wrapper.find('[data-test="stale-badge"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="experiment-strip"]').exists()).toBe(false)
  })

  it('names the time zone of the trend x axis, as the table does', async () => {
    const { state } = setup()
    await state.loadAll()
    await flushPromises()
    expect(chart.options?.scales.x.title).toMatchObject({ display: true, text: 'admin.cacheCharts.trend.time' })
  })

  it('reads only CACHE_ROLLUP_NOT_READY as "not produced yet"; a bare 503 is an error', async () => {
    const bare = setup({ getBreakdown: vi.fn().mockRejectedValue({ status: 503, message: 'Service Unavailable' }) })
    await bare.state.loadAll()
    expect(bare.state.breakdown.status).toBe('error')
    const named = setup({ getBreakdown: vi.fn().mockRejectedValue({ status: 503, reason: 'CACHE_ROLLUP_NOT_READY' }) })
    await named.state.loadAll()
    expect(named.state.breakdown.status).toBe('not_ready')
  })

  it('keeps the precision of a small w_m ($0.075, not $0.08)', async () => {
    expect(formatMoney(0.075, 'USD', 4)).toBe('$0.075')
    expect(formatMoney(1.5, 'USD', 4)).toBe('$1.50')
    const base = createMockCachePanelClient({ now, latencyMs: 0 })
    const getBreakdown = vi.fn(async (...args: Parameters<CachePanelClient['getBreakdown']>) => {
      const data = (await base.getBreakdown(...args)) as BreakdownResponse
      data.rows[0].meta = { ...(data.rows[0].meta ?? {}), w_m: 0.075, priced: true }
      return data
    })
    const { wrapper, state } = setup({ getBreakdown })
    await state.loadAll()
    await flushPromises()
    expect(wrapper.get('[data-test="breakdown-table"]').text()).toContain('"value":"$0.075"')
  })
})

describe('the view before and after the availability probe', () => {
  beforeEach(() => vi.clearAllMocks())

  function pending() {
    let settle!: { resolve: (v: unknown) => void; reject: (e: unknown) => void }
    api.get.mockImplementation(() => new Promise((resolve, reject) => (settle = { resolve, reject })))
    const wrapper = mount(CacheObservabilityView, { global: { stubs } })
    return { wrapper, settle: () => settle }
  }

  it('shows no chart skeleton while it is unknown whether the build has charts, and none when it has not', async () => {
    const { wrapper, settle } = pending()
    await flushPromises()
    expect(wrapper.find('[data-test="availability-pending"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="panel-patrol"]').exists()).toBe(false)
    expect(wrapper.find('[data-status="loading"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="page-description"]').text()).toBe('admin.cacheObservability.description')
    settle().reject({ status: 404 })
    await flushPromises()
    expect(wrapper.find('[data-test="availability-pending"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="panel-patrol"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="settings-stub"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="page-description"]').text()).toBe('admin.cacheObservability.descriptionSettingsOnly')
  })

  it('names the charts in the description only when the build has them', async () => {
    const { wrapper, settle } = pending()
    await flushPromises()
    settle().resolve({ data: mockNorthStar({ window: '7d', platform: 'all' }, now()) })
    await flushPromises()
    expect(wrapper.find('[data-test="panel-patrol"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="page-description"]').text()).toBe('admin.cacheObservability.descriptionCharts')
  })
})
