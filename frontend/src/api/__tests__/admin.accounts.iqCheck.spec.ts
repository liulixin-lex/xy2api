import { describe, expect, it, vi } from 'vitest'
const { get, put, post } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, put, post } }))
import { getIQCheck, getIQCheckModels, listIQResults, runIQCheck, runIQCheckBatch, setIQCheck } from '@/api/admin/accounts'
describe('IQ check configuration API', () => {
  it('reads and patches per-model targets with cancellation', async () => {
    get.mockResolvedValue({ data: { targets: [{ model: 'gpt-6-astra' }] } })
    const signal = new AbortController().signal
    expect(await getIQCheck(8, signal)).toEqual([{ model: 'gpt-6-astra' }])
    expect(get).toHaveBeenCalledWith('/admin/accounts/8/iq-check', { signal })
    put.mockResolvedValue({ data: { targets: [] } })
    await setIQCheck(8, { targets: [{ model: 'gpt-6.1-sol', enabled: false }], remove: ['custom'] })
    expect(put).toHaveBeenCalledWith('/admin/accounts/8/iq-check', { targets: [{ model: 'gpt-6.1-sol', enabled: false }], remove: ['custom'] })
  })
  it('runs all targets, chosen targets or a batch', async () => {
    post.mockResolvedValue({ data: { targets: [], accounts: [{ account_id: 9, reason: 'not_openai', targets: [] }] } })
    await runIQCheck(8)
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/8/iq-check/run', {})
    await runIQCheck(8, ['gpt-6.1-sol'])
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/8/iq-check/run', { models: ['gpt-6.1-sol'] })
    expect(await runIQCheckBatch([8, 9])).toEqual([{ account_id: 9, reason: 'not_openai', targets: [] }])
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/iq-check/run', { account_ids: [8, 9] })
  })
  it('pages results per model and sends the filter only when narrowing', async () => {
    const page = { rounds: [], total: 0, page: 2, page_size: 10 }
    get.mockResolvedValue({ data: page })
    expect(await listIQResults(8, { model: 'gpt-6-astra', page: 2, page_size: 10, filter: '' })).toEqual(page)
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/8/iq-check/results', { params: { model: 'gpt-6-astra', page: 2, page_size: 10 }, signal: undefined })
    await listIQResults(8, { model: 'gpt-6-astra', page: 1, page_size: 10, filter: 'abnormal' })
    expect(get).toHaveBeenLastCalledWith('/admin/accounts/8/iq-check/results', { params: { model: 'gpt-6-astra', page: 1, page_size: 10, filter: 'abnormal' }, signal: undefined })
  })
  it('uses the read-only IQ catalog endpoint with cancellation', async () => {
    const catalog = { models: [], fetched_at: null, from_cache: false, stale: false, error: 'model_discovery_failed' }
    get.mockResolvedValue({ data: catalog })
    const signal = new AbortController().signal
    expect(await getIQCheckModels(8, true, signal)).toEqual(catalog)
    expect(get).toHaveBeenCalledWith('/admin/accounts/8/iq-check/models', { params: { refresh: true }, signal })
  })
})
