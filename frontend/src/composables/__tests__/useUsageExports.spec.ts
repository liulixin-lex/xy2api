import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { useUsageExports } from '../useUsageExports'
const { create, list, action } = vi.hoisted(() => ({ create: vi.fn(), list: vi.fn(), action: vi.fn() }))
vi.mock('@/api/usageExport', () => ({ usageExportAPI: { create, list, action } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: { value: 'en' } }) }))
const queued = { id: 'job-1', status: 'queued', phase: 'queued', format: 'csv' }
const wrappers: ReturnType<typeof mount>[] = []
function harness() {
  let api!: ReturnType<typeof useUsageExports>
  const wrapper = mount(defineComponent({ setup() { api = useUsageExports('user'); return () => null } }))
  wrappers.push(wrapper)
  return { api, wrapper }
}
beforeEach(() => {
  vi.useFakeTimers(); vi.clearAllMocks()
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  list.mockResolvedValue({ items: [], total: 0 }); create.mockResolvedValue(queued)
})
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers(); vi.restoreAllMocks() })
describe('durable export controls', () => {
  it('retries the same frozen creation with the same key and respects Retry-After', async () => {
    const { api } = harness(); await flushPromises()
    create.mockRejectedValueOnce({ status: 429, retryAfterMs: 10000 }).mockResolvedValueOnce(queued)
    const filters = { model: 'original', page: 3, page_size: 100 }
    const pending = api.create(filters); await flushPromises(); filters.model = 'changed'
    await vi.advanceTimersByTimeAsync(9999); expect(create).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(600); await pending
    expect(create).toHaveBeenCalledTimes(2)
    expect(create.mock.calls[0][2]).toBe(create.mock.calls[1][2])
    expect(create.mock.calls[1][1]).toMatchObject({ model: 'original' })
    expect(create.mock.calls[1][1]).not.toHaveProperty('page')
    expect(api.tasks.value[0].id).toBe('job-1')
  })
  it('stops polling while hidden and after unmount without canceling the job', async () => {
    list.mockResolvedValue({ items: [queued], total: 1 })
    const { wrapper } = harness(); await flushPromises(); expect(list).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(15000); expect(list).toHaveBeenCalledTimes(1)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')); await flushPromises(); expect(list).toHaveBeenCalledTimes(2)
    wrapper.unmount(); await vi.advanceTimersByTimeAsync(15000); expect(list).toHaveBeenCalledTimes(2); expect(action).not.toHaveBeenCalled()
  })
  it('does not duplicate rapid submissions and restores a finished job after refresh', async () => {
    const { api } = harness(); await flushPromises()
    let resolve!: (value: unknown) => void
    create.mockReturnValue(new Promise(r => { resolve = r }))
    const first = api.create({}); await api.create({}); expect(create).toHaveBeenCalledTimes(1)
    resolve(queued); await first
    list.mockResolvedValue({ items: [{ ...queued, status: 'succeeded' }], total: 1 })
    await api.refresh(); expect(api.busy.value).toBe(false)
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    action.mockResolvedValue({ url: '/api/v1/usage/exports/job-1/download' })
    await api.action(api.tasks.value[0], 'download-ticket'); expect(click).toHaveBeenCalledOnce()
  })
  it('pauses retries with a refreshable error rather than abandoning the server job', async () => {
    list.mockRejectedValue({ status: 429, retryAfterMs: 180000 })
    const { api } = harness(); await flushPromises()
    expect(api.error.value).not.toBe(''); await vi.advanceTimersByTimeAsync(300000); expect(list).toHaveBeenCalledTimes(1)
  })
})
