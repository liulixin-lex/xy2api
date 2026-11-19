import { flushPromises, shallowMount } from '@vue/test-utils'
import { reactive } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import OpsDashboard from '../OpsDashboard.vue'
import OpsDashboardHeader from '../components/OpsDashboardHeader.vue'
import OpsRequestDetailsModal from '../components/OpsRequestDetailsModal.vue'

const { ops, route } = vi.hoisted(() => ({
  ops: {
    getAdvancedSettings: vi.fn(),
    getDashboardSnapshotV2: vi.fn(),
    getThroughputTrend: vi.fn(),
    getLatencyHistogram: vi.fn(),
    getErrorTrend: vi.fn(),
    getErrorDistribution: vi.fn(),
    getDashboardOverview: vi.fn(),
    getMetricThresholds: vi.fn()
  },
  route: { query: {} as Record<string, string> }
}))
vi.mock('vue-router', () => ({ useRoute: () => reactive(route), useRouter: () => ({ replace: vi.fn().mockResolvedValue(undefined) }) }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: ops, default: ops }))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
  useAdminSettingsStore: () => ({ opsMonitoringEnabled: true, opsQueryModeDefault: 'auto', fetch: vi.fn().mockResolvedValue(undefined) })
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

// Item 1 of the first observe-fe review: a past window picked as a custom range (7 days, then 30) loads the dashboard
// for that window each time, and the request details get the same start and end, not the last hour.
describe('OpsDashboard custom window', () => {
  it('reloads on another custom window and hands the same start and end to the request details', async () => {
    ops.getAdvancedSettings.mockResolvedValue({ display_alert_events: false, display_openai_token_stats: false, auto_refresh_enabled: false, auto_refresh_interval_seconds: 30 })
    ops.getDashboardSnapshotV2.mockResolvedValue({ overview: null, throughput_trend: null, error_trend: null })
    for (const name of ['getThroughputTrend', 'getLatencyHistogram', 'getErrorTrend', 'getErrorDistribution', 'getMetricThresholds'] as const) ops[name].mockResolvedValue(null)
    const wrapper = shallowMount(OpsDashboard, { global: { stubs: { AppLayout: { template: '<div><slot /></div>' } } } })
    await flushPromises()
    const header = wrapper.findComponent(OpsDashboardHeader)
    const windows = [
      { start: '2026-11-04T00:00:00.000Z', end: '2026-11-11T00:00:00.000Z' },
      { start: '2026-10-12T00:00:00.000Z', end: '2026-11-11T00:00:00.000Z' }
    ]
    for (const window of windows) {
      header.vm.$emit('update:customTimeRange', window.start, window.end)
      header.vm.$emit('update:timeRange', 'custom')
      await flushPromises()
      expect(ops.getDashboardSnapshotV2).toHaveBeenLastCalledWith(expect.objectContaining({ start_time: window.start, end_time: window.end }), expect.anything())
      const modal = wrapper.findComponent(OpsRequestDetailsModal)
      expect(modal.props('timeRange')).toBe('custom')
      expect(modal.props('customStartTime')).toBe(window.start)
      expect(modal.props('customEndTime')).toBe(window.end)
    }
    wrapper.unmount()
  })
})
