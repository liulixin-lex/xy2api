import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'
import type { OpsDashboardOverview } from '@/api/admin/ops'

vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { getRealtimeTrafficSummary: vi.fn().mockResolvedValue({ enabled: true, summary: null }) } }))
vi.mock('@/stores', () => ({ useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: true, setOpsRealtimeMonitoringEnabledLocal: vi.fn() }) }))

// Browser check #4 with the real messages: the user-wait tip's request count picks the English plural form, so the call
// must pass the count. Chinese has one form.
async function tip(locale: 'en' | 'zh', count: number) {
  const i18n = createI18n({ legacy: false, locale, messages: { en, zh } as never })
  const overview = { tps: { avg: 1 }, ttft: { p99_ms: 250 }, e2e_first_output: { p99_ms: 1850 }, e2e_first_output_sample_count: count } as OpsDashboardOverview
  const wrapper = mount(OpsDashboardHeader, {
    props: { overview, platform: '', groupId: null, timeRange: '1h', queryMode: 'auto', loading: false, lastUpdated: null },
    global: { plugins: [i18n], stubs: { Select: true, BaseDialog: true, Icon: true, HelpTooltip: { props: ['content'], template: '<i data-testid="help" :data-content="content" />' } } }
  })
  await flushPromises()
  const content = wrapper.get('[data-testid="ops-e2e-card"] [data-testid="help"]').attributes('data-content')!
  wrapper.unmount()
  return content
}

describe('OpsDashboardHeader user-wait tip plural (real messages)', () => {
  it('says "Based on 1 request" for one and "requests" for more', async () => {
    expect(await tip('en', 1)).toContain('Based on 1 request. ')
    expect(await tip('en', 2)).toContain('Based on 2 requests. ')
    expect(await tip('zh', 1)).toContain('基于 1 个请求')
  })
})
