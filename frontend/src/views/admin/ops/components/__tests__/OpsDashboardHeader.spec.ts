import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'
import type { OpsDashboardOverview } from '@/api/admin/ops'

vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { getRealtimeTrafficSummary: vi.fn().mockResolvedValue({ enabled: true, summary: null }) } }))
vi.mock('@/stores', () => ({ useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: true, setOpsRealtimeMonitoringEnabledLocal: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key })
}))

const overview = { tps: { avg: 242.7 } } as OpsDashboardOverview
function mountHeader(value = overview) {
  return mount(OpsDashboardHeader, {
    props: { overview: value, platform: '', groupId: null, timeRange: '1h', queryMode: 'auto', loading: false, lastUpdated: null },
    global: { stubs: { Select: true, HelpTooltip: true, BaseDialog: true, Icon: true } }
  })
}

describe('OpsDashboardHeader user wait', () => {
  const ttft = { p50_ms: 700, p90_ms: 900, p95_ms: 950, p99_ms: 1200, avg_ms: 750, max_ms: 1500 }
  const e2e = { p50_ms: 51000, p90_ms: 60000, p95_ms: 62000, p99_ms: 64000, avg_ms: 40000, max_ms: 70000 }
  const mountWait = (value: Partial<OpsDashboardOverview>) => mount(OpsDashboardHeader, {
    props: {
      overview: { ...overview, ttft, ...value } as OpsDashboardOverview, platform: '', groupId: null, timeRange: '1h', queryMode: 'auto',
      loading: false, lastUpdated: null, thresholds: { ttft_p99_ms_max: 1000 }
    },
    global: { stubs: { Select: true, HelpTooltip: { props: ['content'], template: '<i data-testid="help" :data-content="content" />' }, BaseDialog: true, Icon: true } }
  })

  it('leads with the end-to-end percentiles, says how many requests, and keeps the last attempt red over its threshold', async () => {
    const wrapper = mountWait({ e2e_first_output: e2e, e2e_first_output_sample_count: 1234 })
    await flushPromises()
    const card = wrapper.get('[data-testid="ops-e2e-card"]')
    expect(card.get('[data-testid="ops-e2e-p99"]').text()).toBe('64000')
    expect(card.text()).toContain('51000')
    expect(card.text()).not.toContain('TTFT')
    expect(card.find('[data-testid="ops-e2e-none"]').exists()).toBe(false)
    expect(card.get('[data-testid="help"]').attributes('data-content')).toBe(
      'admin.ops.tooltips.e2eFirstOutput admin.ops.e2eFirstOutput.samples:{"n":"1,234"}'
    )
    const last = card.get('[data-testid="ops-e2e-last-attempt"]')
    expect(last.text()).toContain('admin.ops.e2eFirstOutput.lastAttempt')
    expect(last.text()).toContain('1200')
    expect(last.findAll('.font-bold').map((el) => el.classes().some((c) => c.startsWith('text-red')))).toEqual([true, false])
    wrapper.unmount()
  })

  it.each([
    ['no samples', { e2e_first_output: { p50_ms: null, p90_ms: null, p95_ms: null, p99_ms: null, avg_ms: null, max_ms: null }, e2e_first_output_sample_count: 0 }],
    ['an answer without the fields', {}]
  ])('shows the user wait as unknown with why for %s, the last attempt still shown', async (_, value) => {
    const wrapper = mountWait(value)
    await flushPromises()
    const card = wrapper.get('[data-testid="ops-e2e-card"]')
    expect(card.get('[data-testid="ops-e2e-p99"]').text()).toBe('admin.ops.e2eFirstOutput.unknown')
    expect(card.get('[data-testid="ops-e2e-none"]').text()).toBe('admin.ops.e2eFirstOutput.none')
    expect(card.get('[data-testid="help"]').attributes('data-content')).toBe('admin.ops.tooltips.e2eFirstOutput admin.ops.e2eFirstOutput.none')
    expect(card.get('[data-testid="ops-e2e-last-attempt"]').text()).toContain('1200')
    wrapper.unmount()
  })
})

describe('OpsDashboardHeader output TPS', () => {

  it('shows per-request P50, slower-tail percentiles and sample count separately from traffic TPS', async () => {
    const wrapper = mountHeader({ ...overview, output_tps: { p5: 2.35, p10: 3.7, p50: 55, avg: 277.75, sample_count: 4 } })
    await flushPromises()
    const card = wrapper.get('[data-testid="output-tps-card"]')
    expect(card.get('[data-testid="output-tps-p50"]').text()).toBe('55.0')
    expect(card.get('[data-testid="output-tps-p5"]').text()).toBe('2.4')
    expect(card.get('[data-testid="output-tps-p10"]').text()).toBe('3.7')
    expect(card.get('[data-testid="output-tps-avg"]').text()).toBe('277.8')
    expect(card.get('[data-testid="output-tps-samples"]').text()).toContain('4')
    expect(card.text()).toContain('tokens/s (P50)')
    expect(card.text()).not.toContain('242.7')
    await wrapper.setProps({ fullscreen: true, overview: { ...overview, output_tps: { p5: 0.01, p10: 0.02, p50: 0.04, avg: 0.05, sample_count: 1 } } })
    expect(card.get('[data-testid="output-tps-p50"]').text()).toBe('0.0')
    expect(card.get('[data-testid="output-tps-samples"]').text()).toContain('1')
    wrapper.unmount()
  })

  it.each([undefined, null, { p5: null, p10: null, p50: null, avg: null, sample_count: 0 }])('shows missing output TPS as unavailable, not zero: %s', async (output_tps) => {
    const wrapper = mountHeader({ ...overview, output_tps })
    await flushPromises()
    for (const metric of ['p5', 'p10', 'p50', 'avg']) {
      expect(wrapper.get(`[data-testid="output-tps-${metric}"]`).text()).toBe('—')
    }
    expect(wrapper.get('[data-testid="output-tps-samples"]').text()).toContain(output_tps == null ? '—' : '0')
    wrapper.unmount()
  })

})
