import { describe, expect, it, vi } from 'vitest'
const { get, put } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, put } }))
import { getIQCheckModels, getIQCheckStatus, setIQCheck } from '@/api/admin/accounts'
describe('IQ check configuration API', () => {
  it('loads a read-only status summary with cancellation', async () => {
    get.mockResolvedValue({ data: { enabled: true, execution_state: 'deferred' } })
    const signal = new AbortController().signal
    expect(await getIQCheckStatus(8, signal)).toEqual({ enabled: true, execution_state: 'deferred' })
    expect(get).toHaveBeenCalledWith('/admin/accounts/8/iq-check', { signal })
  })
  it('sends a partial setting without defaulting omitted fields', async () => {
    put.mockResolvedValue({ data: { enabled: false } })
    await setIQCheck(8, { enabled: false })
    expect(put).toHaveBeenCalledWith('/admin/accounts/8/iq-check', { enabled: false })
  })
  it('uses the read-only IQ catalog endpoint with cancellation', async () => {
    const catalog = { models: [], fetched_at: null, from_cache: false, stale: false, error: 'model_discovery_failed' }
    get.mockResolvedValue({ data: catalog })
    const signal = new AbortController().signal
    expect(await getIQCheckModels(8, true, signal)).toEqual(catalog)
    expect(get).toHaveBeenCalledWith('/admin/accounts/8/iq-check/models', { params: { refresh: true }, signal })
  })
})
