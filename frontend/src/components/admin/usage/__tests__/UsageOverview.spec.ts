// Mobile list plan, usage records: below 1024px the stats cards and charts start collapsed to the date range, a one-line
// summary and "Show charts"; the date range stays outside the collapsed part (it also filters the list); from 1024px
// nothing changes.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageOverview from '../UsageOverview.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

const stats = {
  total_requests: 12002, total_input_tokens: 1, total_output_tokens: 1, total_cache_tokens: 0, total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0, total_tokens: 652_440_000, total_cost: 30750.1961, total_actual_cost: 30750.1961,
  average_duration_ms: 0
}

function setWide(wide: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
    matches: query === '(min-width: 1024px)' ? wide : false,
    media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) as unknown as MediaQueryList)
}

const ChartStub = { name: 'ChartStub', template: '<div data-test="chart" />' }
const mountOverview = (props: Record<string, unknown> = {}) => mount(UsageOverview, {
  props: { stats: stats as never, ...props },
  slots: {
    range: '<div data-test="range">range</div>',
    granularity: '<div data-test="granularity">granularity</div>',
    charts: ChartStub,
  },
  global: { stubs: { UsageStatsCards: { name: 'UsageStatsCards', template: '<div data-test="cards" />' }, Icon: true } },
})

afterEach(() => { vi.restoreAllMocks() })

describe('UsageOverview', () => {
  it('starts collapsed below 1024px: date range, summary and toggle; no cards or charts mounted', () => {
    setWide(false)
    const w = mountOverview()
    expect(w.find('[data-test="range"]').exists()).toBe(true)
    expect(w.find('[data-test="granularity"]').exists()).toBe(false)
    expect(w.find('[data-test="cards"]').exists()).toBe(false)
    expect(w.findComponent(ChartStub).exists()).toBe(false)
    // Each label stays with its value (a pair wraps whole); the toggle sits beside the date range.
    const pairs = w.get('[data-testid="usage-overview-summary"]').findAll('span.whitespace-nowrap').map((s) => s.text())
    expect(pairs).toEqual(['usage.overview.requests 12,002', 'usage.overview.tokens 652.44M', 'usage.overview.cost $30,750.20'])
    expect(w.get('[data-test="range"]').element.parentElement).toBe(w.get('[data-testid="usage-overview-toggle"]').element.parentElement)
    const toggle = w.get('[data-testid="usage-overview-toggle"]')
    expect(toggle.text()).toBe('usage.overview.expand')
    expect(toggle.attributes('aria-expanded')).toBe('false')
  })

  it('mounts the cards, granularity and charts when expanded, keeping the date range where it was', async () => {
    setWide(false)
    const w = mountOverview()
    const toggle = w.get('[data-testid="usage-overview-toggle"]')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(toggle.text()).toBe('usage.overview.collapse')
    const panel = w.get('[data-testid="usage-overview-charts"]')
    expect(toggle.attributes('aria-controls')).toBe(panel.attributes('id'))
    expect(panel.find('[data-test="cards"]').exists()).toBe(true)
    expect(panel.findComponent(ChartStub).exists()).toBe(true)
    expect(w.get('[data-testid="usage-overview-compact"]').find('[data-test="range"]').exists()).toBe(true)
    expect(panel.find('[data-test="range"]').exists()).toBe(false)
    expect(w.find('[data-test="granularity"]').exists()).toBe(true)

    await toggle.trigger('click')
    expect(w.findComponent(ChartStub).exists()).toBe(false)
  })

  it('shows "…" on the admin page while the filter\'s stats load, as the cards do', () => {
    setWide(false)
    const w = mountOverview({ e2eState: 'loading' })
    const values = w.get('[data-testid="usage-overview-summary"]').findAll('span.font-medium').map((s) => s.text())
    expect(values).toEqual(['…', '…', '…'])
  })

  it('keeps the desktop layout from 1024px: cards, range with granularity, charts, no toggle', () => {
    setWide(true)
    const w = mountOverview({ e2eState: 'loaded' })
    expect(w.find('[data-test="cards"]').exists()).toBe(true)
    expect(w.find('[data-test="range"]').exists()).toBe(true)
    expect(w.find('[data-test="granularity"]').exists()).toBe(true)
    expect(w.findComponent(ChartStub).exists()).toBe(true)
    expect(w.find('[data-testid="usage-overview-toggle"]').exists()).toBe(false)
  })
})
