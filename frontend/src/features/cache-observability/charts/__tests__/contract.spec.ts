// Chart endpoint contract tests (00-shared/cache_panel_contract v1.0.2, P-1 to P-10, R1 to R16).
// F's samples run through the real apiClient (stub adapter), the API layer, useCacheCharts and the
// rendered panel, so envelope unwrapping, error rejection and guard.ts are the production ones.
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { InternalAxiosRequestConfig } from 'axios'

const chart = vi.hoisted(() => ({ data: null as null | { datasets: { data: (number | null)[] }[] } }))

vi.mock('vue-i18n', async () => {
  const zh = (await import('@/i18n/locales/zh')).default as Record<string, unknown>
  const exists = (key: string) => key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], zh) !== undefined
  return {
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
      te: exists,
    }),
  }
})
vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options', 'plugins'],
    setup(props: { data: typeof chart.data }) {
      chart.data = props.data
      return {}
    },
    template: '<canvas data-test="line-chart" />',
  },
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ replace: vi.fn() }), onBeforeRouteLeave: vi.fn() }))
vi.mock('@/i18n', () => ({ getLocale: () => 'zh-CN' }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getAllIncludingInactive: vi.fn(async () => []) } } }))
// The view test only needs the tab logic; the layout and the switches pull in the whole app shell.
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('../../settings/CacheSettingsPanel.vue', () => ({ default: { template: '<div data-test="settings-stub" />' } }))

import { apiClient } from '@/api/client'
import CacheObservabilityView from '../../CacheObservabilityView.vue'
import ChartsPanel from '../ChartsPanel.vue'
import { getBreakdown, liveCachePanelClient } from '../api'
import type { BreakdownResponse, NorthStarResponse, TraceStatsResponse } from '../types'
import { useCacheCharts, type CacheChartsState, type SectionStatus } from '../useCacheCharts'

interface Sample {
  _contract: string[]
  _note: string
  request: { method: string; path: string; body: unknown }
  status: number
  response: Record<string, any>
}

type Endpoint = 'northstar' | 'breakdown' | 'stats' | 'experiments'

const valid = import.meta.glob('./contract/*.json', { eager: true, import: 'default' }) as Record<string, Sample>
const invalid = import.meta.glob('./contract/invalid/*.json', { eager: true, import: 'default' }) as Record<string, Sample>
const VALID_NAMES = Object.keys(valid).map((p) => p.replace('./contract/', '').replace('.json', ''))
const INVALID_NAMES = Object.keys(invalid).map((p) => p.replace('./contract/invalid/', '').replace('.json', ''))

function sample(name: string): Sample {
  const found = valid[`./contract/${name}.json`] ?? invalid[`./contract/invalid/${name}.json`]
  if (!found) throw new Error(`unknown contract sample ${name}`)
  return structuredClone(found)
}

function endpointOf(s: Sample): Endpoint {
  return s.request.path.split('?')[0].split('/').pop() as Endpoint
}

function queryOf(s: Sample): URLSearchParams {
  return new URLSearchParams(s.request.path.split('?')[1] ?? '')
}

/** A success answer derived from a sample: only values differ, never the shape. */
function variant(name: string, change: (data: any) => void): Sample {
  const s = sample(name)
  change(s.response.data)
  return s
}

/** P-10: what each endpoint accepts; anything else is a 400. */
const ALLOWED: Record<Endpoint, string[]> = {
  northstar: ['window', 'platform', 'granularity', 'cost_view'],
  breakdown: ['window', 'platform', 'dimension', 'sort', 'limit', 'cost_view'],
  stats: ['window', 'platform'],
  experiments: [],
}

/** `params` are what reaches the server (the built URL); `given` is the config before serialization. */
let calls: { endpoint: Endpoint; params: Record<string, string>; given: Record<string, unknown> }[] = []
let answers: Partial<Record<Endpoint, Sample>> = {}

beforeEach(() => {
  calls = []
  answers = {}
  chart.data = null
  apiClient.defaults.adapter = vi.fn(async (config: InternalAxiosRequestConfig) => {
    const url = new URL(apiClient.getUri(config), 'http://panel.test')
    const endpoint = url.pathname.split('/').pop() as Endpoint
    calls.push({ endpoint, params: Object.fromEntries(url.searchParams), given: { ...(config.params ?? {}) } })
    const s = answers[endpoint]
    if (!s) throw new Error(`unexpected request ${config.url}`)
    const data = structuredClone(s.response)
    if (s.status < 400) return { status: s.status, statusText: 'OK', headers: {}, config, data }
    throw { response: { status: s.status, data, headers: {} }, config, code: 'ERR_BAD_REQUEST' }
  })
})
enableAutoUnmount(afterEach)

const BASE: Record<Endpoint, string> = {
  northstar: 'northstar_7d_list',
  breakdown: 'breakdown_group',
  stats: 'stats_24h',
  experiments: 'experiments_empty_m2',
}

/** Answers every endpoint (the base samples unless overridden), sets the filters the sample under test asked with, renders the panel. */
async function render(under?: Sample) {
  answers = Object.fromEntries(Object.entries(BASE).map(([e, name]) => [e, sample(name)])) as Record<Endpoint, Sample>
  if (under) answers[endpointOf(under)] = under
  const state = useCacheCharts({ client: liveCachePanelClient, source: 'live', loadGroups: async () => [] })
  if (under) applyFilters(state, under)
  const wrapper = mount(ChartsPanel, { props: { state }, global: { stubs: { Icon: true, HelpTooltip: true } } })
  await state.loadAll()
  await flushPromises()
  return { state, wrapper }
}

function applyFilters(state: CacheChartsState, s: Sample) {
  const q = queryOf(s)
  const endpoint = endpointOf(s)
  if (q.get('platform')) state.setFilter('platform', q.get('platform') as never)
  if (endpoint === 'stats') {
    if (q.get('window')) state.setFilter('traceWindow', q.get('window') as never)
    return
  }
  if (q.get('window')) state.setFilter('summaryWindow', q.get('window') as never)
  state.setFilter('costView', (q.get('cost_view') ?? 'list') as never)
  if (q.get('dimension')) state.setFilter('dimension', q.get('dimension') as never)
  if (q.get('sort')) state.setFilter('sort', q.get('sort') as never)
}

function statusOf(wrapper: ReturnType<typeof mount>, section: string): string | undefined {
  return wrapper.get(`[data-test="section-${section}"]`).attributes('data-status')
}

/** No broken numbers anywhere on screen, and a drawn chart only from finite points. */
function expectClean(wrapper: ReturnType<typeof mount>, name: string) {
  expect(wrapper.text(), name).not.toMatch(/NaN|Infinity|undefined|\[object Object\]/)
  expect(wrapper.html(), name).not.toMatch(/NaN|Infinity/)
  for (const set of chart.data?.datasets ?? []) {
    expect(set.data.every((v) => v === null || (typeof v === 'number' && Number.isFinite(v))), name).toBe(true)
  }
}

const SECTION: Record<Endpoint, string> = { northstar: 'northstar', breakdown: 'breakdown', stats: 'loss', experiments: 'experiments' }

describe('cache panel contract samples v1.0.2', () => {
  it('has the 21 valid samples and the 24 that must be refused or degraded', () => {
    expect(VALID_NAMES).toHaveLength(21)
    expect(INVALID_NAMES.sort()).toEqual(Object.keys(INVALID_EXPECT).sort())
  })

  it.each(VALID_NAMES.filter((n) => sample(n).status < 400))('renders %s through the API layer without broken numbers or an empty chart', async (name) => {
    const s = sample(name)
    const endpoint = endpointOf(s)
    const { state, wrapper } = await render(s)
    expect(statusOf(wrapper, SECTION[endpoint]), name).toBe('ready')
    expectClean(wrapper, name)

    // P-10: only known parameters, the values the sample asked with, and never granularity (P-4).
    const sent = calls.filter((c) => c.endpoint === endpoint).pop()!.params
    expect(Object.keys(sent).every((k) => ALLOWED[endpoint].includes(k)), name).toBe(true)
    expect(sent).not.toHaveProperty('granularity')
    for (const [key, value] of queryOf(s)) if (key !== 'limit' && key !== 'granularity') expect(String(sent[key]), `${name} ${key}`).toBe(value)

    if (endpoint === 'northstar') {
      const data = state.northstar.data as NorthStarResponse
      if (data.series.length) {
        expect(wrapper.find('[data-test="line-chart"]').exists(), name).toBe(true)
        expect(chart.data!.datasets.some((set) => set.data.some((v) => typeof v === 'number')), name).toBe(true)
      } else {
        expect(wrapper.find('[data-test="line-chart"]').exists(), name).toBe(false)
        expect(wrapper.text(), name).toContain('admin.cacheCharts.trend.empty')
      }
      if (data.summary.cost === null) expect(wrapper.get('[data-test="northstar-cost"]').text()).toContain('admin.cacheCharts.cost.unavailable')
    }
    if (endpoint === 'breakdown') {
      const data = state.breakdown.data as BreakdownResponse
      expect(wrapper.findAll('[data-test="breakdown-table"] tbody tr').length, name).toBeGreaterThanOrEqual(data.rows.length)
      // Labels show as sent, including the masked `<platform>#<account_id>` of R16.
      const table = wrapper.get('[data-test="breakdown-table"]').text()
      for (const row of data.rows) expect(table, `${name} ${row.key}`).toContain(row.label)
    }
    if (endpoint === 'stats') {
      const data = state.stats.data as TraceStatsResponse
      expect(wrapper.findAll('[data-test="trace-disabled"]').length > 0, name).toBe(!data.trace_enabled)
    }
  })

  it('shows a ranking whose entries were all merged into other, and says "no data" only when there is none (§1.1)', async () => {
    const merged = variant('breakdown_group', (data) => {
      data.rows = []
    })
    expect(merged.response.data.other).not.toBeNull()
    let { wrapper } = await render(merged)
    const table = wrapper.get('[data-test="breakdown-table"]')
    expect(table.findAll('tbody tr')).toHaveLength(1)
    expect(table.text()).toContain('admin.cacheCharts.breakdown.other')
    expect(table.text()).toContain('admin.cacheCharts.columns.saved')
    expect(wrapper.get('[data-test="section-breakdown"]').text()).not.toContain('admin.cacheCharts.breakdown.empty')
    expectClean(wrapper, 'merged into other')
    ;({ wrapper } = await render(variant('breakdown_group', (data) => {
      data.rows = []
      data.other = null
    })))
    expect(wrapper.find('[data-test="breakdown-table"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="section-breakdown"]').text()).toContain('admin.cacheCharts.breakdown.empty')
  })

  it('shows a previous window without a ratio as a placeholder (R5)', async () => {
    const s = variant('northstar_7d_list', (data) => {
      data.summary.previous = { token_hit_upper: null, token_hit_lower: null, request_hit: null }
    })
    const { wrapper } = await render(s)
    expect(wrapper.get('[data-test="northstar-delta"]').text()).toContain('"delta":"-"')
    expectClean(wrapper, 'R5')
  })

  it('maps 503 CACHE_ROLLUP_NOT_READY to "not produced yet" and keeps the tab (P-9)', async () => {
    for (const endpoint of ['northstar', 'breakdown'] as const) {
      const s = sample('error_503_rollup_not_ready')
      s.request.path = s.request.path.replace('/northstar', `/${endpoint}`)
      const { state, wrapper } = await render(s)
      expect(state.available.value, endpoint).toBe(true)
      expect(statusOf(wrapper, endpoint), endpoint).toBe('not_ready')
      expect(wrapper.get(`[data-test="section-${endpoint}"]`).text()).toContain('admin.cacheCharts.state.notReady')
    }
  })

  it('shows 400 and 403 as failures with the server message (P-9)', async () => {
    const bad = sample('error_400_invalid_param')
    bad.request.path = bad.request.path.replace(/\?.*$/, '')
    let { wrapper } = await render(bad)
    expect(statusOf(wrapper, 'breakdown')).toBe('error')
    expect(wrapper.get('[data-test="section-breakdown"]').text()).toContain('invalid cache panel query')
    ;({ wrapper } = await render(sample('error_403_non_admin')))
    expect(statusOf(wrapper, 'northstar')).toBe('error')
    expect(wrapper.get('[data-test="section-northstar"]').text()).toContain('Admin access required')
  })

  it('hides the daily-check tab when the chart endpoints are not deployed (404), and shows it when they answer', async () => {
    const stubs = { ChartsPanel: true, Icon: true }
    answers = { northstar: { _contract: [], _note: 'not deployed', request: { method: 'GET', path: '/api/v1/admin/cache/northstar', body: null }, status: 404, response: { code: 404, message: 'Not Found' } } }
    const hidden = mount(CacheObservabilityView, { global: { stubs } })
    await flushPromises()
    expect(hidden.find('[role="tablist"]').exists()).toBe(false)
    expect(hidden.find('[data-test="settings-heading"]').exists()).toBe(true)
    expect(calls.map((c) => c.endpoint)).toEqual(['northstar'])

    calls = []
    answers = Object.fromEntries(Object.entries(BASE).map(([e, name]) => [e, sample(name)])) as Record<Endpoint, Sample>
    const shown = mount(CacheObservabilityView, { global: { stubs } })
    await flushPromises()
    expect(shown.find('[role="tablist"]').exists()).toBe(true)
  })

  it('never sends a parameter to experiments (P-8, R14)', async () => {
    await render()
    expect(calls.find((c) => c.endpoint === 'experiments')!.params).toEqual({})
  })

  it('leaves the platform timezone out of the URL, since the endpoints refuse unknown parameters (P-10, R15)', async () => {
    await render()
    for (const call of calls) {
      expect(call.given, call.endpoint).toHaveProperty('timezone')
      expect(call.params, call.endpoint).not.toHaveProperty('timezone')
    }
  })

  it('never sends the timezone R15 refuses, and shows that 400 as a failure if it comes (P-10, R15)', async () => {
    const refused = sample('error_400_timezone_param')
    expect(queryOf(refused).get('timezone')).toBeTruthy()
    const { wrapper } = await render(refused)
    const sent = calls.find((c) => c.endpoint === 'northstar')!
    expect(sent.given).toHaveProperty('timezone')
    expect(sent.params).not.toHaveProperty('timezone')
    // The same window and platform as the refused request, without the timezone.
    for (const [key, value] of queryOf(refused)) if (key !== 'timezone') expect(sent.params[key], key).toBe(value)
    expect(statusOf(wrapper, 'northstar')).toBe('error')
    expect(wrapper.get('[data-test="section-northstar"]').text()).toContain('invalid cache panel query')
  })

  it('keeps at most the requested rows (P-6)', async () => {
    answers = { breakdown: sample('breakdown_rows_over_limit') }
    const data = await getBreakdown({ dimension: 'model', window: '7d', platform: 'all', sort: 'opportunity_amount', limit: 2 })
    expect(data.rows).toHaveLength(2)
    expect(calls[0].params.limit).toBe('2')
  })
})

type Outcome = { status: SectionStatus; check?: (r: Awaited<ReturnType<typeof render>>) => void }

/** Each counter-example breaks one rule; the panel refuses identity data and shows anything misleading as not computed. */
const INVALID_EXPECT: Record<string, Outcome> = {
  // P-3: refused whole, nothing of it reaches the screen.
  breakdown_is_fallback: { status: 'rejected', check: ({ wrapper }) => expect(wrapper.find('[data-test="breakdown-table"]').exists()).toBe(false) },
  breakdown_privacy_api_key_id: { status: 'rejected', check: ({ wrapper }) => expect(wrapper.find('[data-test="breakdown-table"]').exists()).toBe(false) },
  breakdown_privacy_hmac_label: { status: 'rejected', check: ({ wrapper }) => expect(wrapper.html()).not.toContain('v2:5f2c9a1e7b3d4068') },
  // R16: an account name shaped like an email must arrive masked; sent as is, the ranking is withheld.
  breakdown_email_label: { status: 'rejected', check: ({ wrapper }) => expect(wrapper.html()).not.toMatch(/[^@\s]+@[^@\s]+\.[A-Za-z]+/) },
  // R16: a masked label that disagrees with key or platform carries no identity; the panel cannot tell which side is wrong.
  breakdown_masked_label_mismatch: { status: 'ready' },
  // P-6, §8.3: degraded to what the contract allows.
  breakdown_meta_null: { status: 'ready', check: ({ state }) => expect(state.breakdown.data!.rows[0].meta).toEqual({}) },
  breakdown_rows_over_limit: { status: 'ready' },
  breakdown_unpriced_with_w_m: {
    status: 'ready',
    check: ({ state, wrapper }) => {
      expect(state.breakdown.data!.rows[1].meta).not.toHaveProperty('w_m')
      const row = wrapper.findAll('[data-test="breakdown-table"] tbody tr')[1].text()
      expect(row).toContain('admin.cacheCharts.breakdown.unpriced')
      expect(row).not.toContain('admin.cacheCharts.breakdown.wm')
    },
  },
  // P-9: errors stay errors; the 503 is "not produced yet" whatever it carries.
  error_400_wrong_reason: { status: 'error' },
  error_503_with_data: { status: 'not_ready' },
  // P-4, P-5, §1: a ratio out of range is not computed; the rest renders as sent.
  northstar_ratio_out_of_range: {
    status: 'ready',
    check: ({ state, wrapper }) => {
      expect(state.northstar.data!.summary.token_hit_upper).toBeNull()
      expect(wrapper.get('[data-test="northstar-hero"]').text()).not.toMatch(/1[0-9]{2}(\.\d+)?%/)
    },
  },
  northstar_unknown_note: { status: 'ready', check: ({ wrapper }) => expect(wrapper.get('[data-test="cost-notes"]').text()).toContain('pricing_mismatch_models') },
  northstar_upstream_share_with_list: {
    status: 'ready',
    check: ({ state, wrapper }) => {
      expect(state.northstar.data!.summary.cost!.upstream_covered_p_share).toBeNull()
      expect(wrapper.get('[data-test="cost-basis"]').text()).not.toContain('admin.cacheCharts.cost.upstreamCovered')
    },
  },
  northstar_missing_cost_key: { status: 'ready', check: ({ wrapper }) => expect(wrapper.get('[data-test="northstar-cost"]').text()).toContain('admin.cacheCharts.cost.unavailable') },
  northstar_series_missing_amount: { status: 'ready' },
  northstar_removed_pricing_field: { status: 'ready', check: ({ wrapper }) => expect(wrapper.html()).not.toContain('pricing_snapshot_at') },
  northstar_granularity_default_wrong: { status: 'ready' },
  northstar_view_echo_mismatch: { status: 'ready' },
  northstar_window_end_not_as_of: { status: 'ready' },
  // A zero where null was due cannot be told apart on the client; it renders as 0.
  northstar_zero_instead_of_null: { status: 'ready' },
  // P-7: tracing off means no trace data, whatever the arrays hold; n/a classes are coverage gaps.
  stats_disabled_with_data: {
    status: 'ready',
    check: ({ wrapper }) => {
      expect(wrapper.findAll('[data-test="trace-disabled"]').length).toBeGreaterThan(0)
      expect(wrapper.find('[data-test="waterfall"]').exists()).toBe(false)
    },
  },
  stats_na_not_excluded: {
    status: 'ready',
    check: ({ state }) => {
      const na = state.stats.data!.loss_classes.filter((c) => c.class.startsWith('n/a_'))
      expect(na.length).toBeGreaterThan(0)
      expect(na.every((c) => c.excluded && c.opportunity_tokens === null)).toBe(true)
    },
  },
  stats_missing_policy_version: { status: 'ready' },
  // R1: the envelope is unwrapped on code 0; the message text is not checked.
  success_message_ok: { status: 'ready' },
}

describe('cache panel contract counter-examples v1.0.2', () => {
  it.each(INVALID_NAMES)('handles %s by its contract meaning', async (name) => {
    const s = sample(name)
    const expected = INVALID_EXPECT[name]
    const rendered = await render(s)
    expect(statusOf(rendered.wrapper, SECTION[endpointOf(s)]), name).toBe(expected.status)
    expectClean(rendered.wrapper, name)
    expected.check?.(rendered)
  })
})
