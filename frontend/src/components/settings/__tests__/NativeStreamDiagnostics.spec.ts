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
  const diagnostic = (started_at: string) => ({ data: { delivery: { started_at, metric_version: 'native_stream_v1', attempts: [] } } })
  it('clears loaded data when the request or authorization scope changes', async () => {
    let resolveNext!: (value: unknown) => void
    get.mockResolvedValueOnce(diagnostic('FIRST_REQUEST_ONLY')).mockImplementationOnce(() => new Promise(resolve => { resolveNext = resolve }))
    const wrapper = await open()
    expect(wrapper.text()).toContain('FIRST_REQUEST_ONLY')
    await wrapper.setProps({ requestId: 'second-request', apiKeyId: 9 })
    await flushPromises()
    expect(wrapper.text()).not.toContain('FIRST_REQUEST_ONLY')
    expect(get).toHaveBeenCalledTimes(2)
    resolveNext(diagnostic('SECOND_REQUEST_ONLY')); await flushPromises()
    expect(wrapper.text()).toContain('SECOND_REQUEST_ONLY')
    wrapper.unmount()
  })
  it('fences a late old response after selecting another request', async () => {
    let resolveOld!: (value: unknown) => void
    get.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve })).mockResolvedValueOnce(diagnostic('CURRENT_REQUEST_ONLY'))
    const wrapper = await open()
    await wrapper.setProps({ requestId: 'new-request', groupId: 8 })
    await flushPromises()
    resolveOld(diagnostic('STALE_REQUEST_ONLY')); await flushPromises()
    expect(wrapper.text()).toContain('CURRENT_REQUEST_ONLY')
    expect(wrapper.text()).not.toContain('STALE_REQUEST_ONLY')
    wrapper.unmount()
  })
  it('aborts an outstanding diagnostics fetch on unmount', async () => {
    get.mockImplementation(() => new Promise(() => {}))
    const wrapper = await open()
    const signal = get.mock.calls[0]?.[1]?.signal as AbortSignal | undefined
    expect(signal).toBeDefined()
    wrapper.unmount()
    expect(signal?.aborted).toBe(true)
  })
  it('shows missing content timing independently of created and never paints it as first content', async () => {
    get.mockResolvedValue({ data: { delivery: { started_at: '2026-09-30T00:00:00Z', metric_version: 'native_stream_v1', gateway_read_to_flush_ms: 0.012, attempts: [{ first_event_ms: 200, account_id: 9, priority: 1, reason: 'protocol_owner', outcome: 'stream_error' }] } } })
    const wrapper = await open()
    expect(get).toHaveBeenCalledWith('/admin/ops/requests/request-fixture/native-stream', { params: { user_id: 1, api_key_id: 2, group_id: 3 }, signal: expect.any(AbortSignal) })
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
