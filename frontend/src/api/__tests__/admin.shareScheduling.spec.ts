import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import shareSchedulingAPI from '../admin/shareScheduling'
import { sharePolicy } from '@/stores/__tests__/shareSchedulingFixtures'

afterEach(() => vi.restoreAllMocks())

describe('share scheduling admin API', () => {
  it('uses the share-scheduling endpoints and payloads', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: [] })
    const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: {} })
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { mode: 'cold', cleared: 3 } })
    const policy = sharePolicy({ scheduler: 'share' })

    await shareSchedulingAPI.getSettings()
    await shareSchedulingAPI.updateSettings({ enabled: true })
    await shareSchedulingAPI.listGroups()
    await shareSchedulingAPI.getPolicy(5)
    await shareSchedulingAPI.updatePolicy(5, 0, policy)
    await shareSchedulingAPI.preview(5, policy)
    await shareSchedulingAPI.explain(5, { model: 'm', session_hash: 's' })
    await shareSchedulingAPI.report(5, '6h')
    await shareSchedulingAPI.decisions(5, 50)
    expect(await shareSchedulingAPI.rebalance(5, 'cold')).toEqual({ mode: 'cold', cleared: 3 })
    await shareSchedulingAPI.accountRules(12)

    expect(get.mock.calls.map(call => call[0])).toEqual([
      '/admin/share-scheduling/settings',
      '/admin/share-scheduling/groups',
      '/admin/share-scheduling/groups/5/policy',
      '/admin/share-scheduling/groups/5/report',
      '/admin/share-scheduling/groups/5/decisions',
      '/admin/share-scheduling/accounts/12/rules'
    ])
    expect(get).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/report', { params: { window: '6h' } })
    expect(get).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/decisions', { params: { limit: 50 } })
    expect(put).toHaveBeenCalledWith('/admin/share-scheduling/settings', { enabled: true })
    expect(put).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/policy', { expected_version: 0, policy })
    expect(post).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/preview', { policy })
    expect(post).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/explain', { model: 'm', session_hash: 's' })
    expect(post).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/rebalance', { mode: 'cold' })
  })

  it('returns empty lists for empty responses', async () => {
    vi.spyOn(apiClient, 'get').mockResolvedValue({ data: null })
    expect(await shareSchedulingAPI.listGroups()).toEqual([])
    expect(await shareSchedulingAPI.decisions(5)).toEqual([])
    expect(await shareSchedulingAPI.accountRules(12)).toEqual([])
  })
})
