import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import NativeStreamDiagnostics from '../NativeStreamDiagnostics.vue'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
beforeEach(() => vi.resetAllMocks())
async function open() {
  const wrapper = mount(NativeStreamDiagnostics, { props: { requestId: 'request-fixture', userId: 1, apiKeyId: 2, groupId: 3 } })
  expect(get).not.toHaveBeenCalled()
  wrapper.get('details').element.open = true
  await wrapper.get('details').trigger('toggle'); await flushPromises()
  return wrapper
}
describe('truthful native stream diagnostics', () => {
  it('shows missing content timing independently of created and never paints it as first content', async () => {
    get.mockResolvedValue({ data: { delivery: { started_at: '2026-09-30T00:00:00Z', metric_version: 'native_stream_v1', gateway_read_to_flush_ms: 0.012, attempts: [{ first_event_ms: 200, account_id: 9, priority: 1, reason: 'protocol_owner', outcome: 'stream_error' }] } } })
    const wrapper = await open()
    expect(get).toHaveBeenCalledWith('/admin/ops/requests/request-fixture/native-stream', { params: { user_id: 1, api_key_id: 2, group_id: 3 } })
    const rows = wrapper.findAll('dt').map(dt => [dt.text(), dt.element.nextElementSibling?.textContent])
    expect(rows).toContainEqual(['admin.scheduling.nativeMetrics.firstEvent', '200 ms'])
    expect(rows).toContainEqual(['admin.scheduling.nativeMetrics.firstContent', 'admin.scheduling.nativeMetrics.unknown'])
    expect(wrapper.text()).toContain('0.012 ms')
  })
  it('labels historical or expired data without fabricating timestamps', async () => {
    get.mockRejectedValue({ status: 404 })
    const wrapper = await open()
    expect(wrapper.text()).toContain('admin.scheduling.nativeMetrics.legacy')
    expect(wrapper.find('dl').exists()).toBe(false)
  })
})
