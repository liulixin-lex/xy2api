import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import shareSchedulingAPI from '../admin/shareScheduling'

afterEach(() => vi.restoreAllMocks())

describe('share scheduling admin API', () => {
  it('uses the share-scheduling endpoints and payloads', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: [] })
    const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: {} })
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: {} })
    const update = { scheduler: 'share' as const, session_keep: true, expected_version: 3 }

    await shareSchedulingAPI.getSettings()
    await shareSchedulingAPI.updateSettings({ enabled: true })
    await shareSchedulingAPI.listGroups()
    await shareSchedulingAPI.getPolicy(5)
    await shareSchedulingAPI.updatePolicy(5, update)
    await shareSchedulingAPI.preview(5, update)
    await shareSchedulingAPI.explain(5, { model: 'm', session_hash: 's' })
    await shareSchedulingAPI.report(5, '6h')
    await shareSchedulingAPI.accountRules(12)

    expect(get.mock.calls.map(call => call[0])).toEqual([
      '/admin/share-scheduling/settings',
      '/admin/share-scheduling/groups',
      '/admin/share-scheduling/groups/5/policy',
      '/admin/share-scheduling/groups/5/report',
      '/admin/share-scheduling/accounts/12/rules'
    ])
    expect(get).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/report', { params: { window: '6h' } })
    expect(put).toHaveBeenCalledWith('/admin/share-scheduling/settings', { enabled: true })
    expect(put).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/policy', update)
    // The preview takes the two settings only.
    expect(post).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/preview', { scheduler: 'share', session_keep: true })
    expect(post).toHaveBeenCalledWith('/admin/share-scheduling/groups/5/explain', { model: 'm', session_hash: 's' })
    expect(Object.keys(shareSchedulingAPI)).not.toContain('rebalance')
    expect(Object.keys(shareSchedulingAPI)).not.toContain('decisions')
  })

  it('returns empty lists for empty responses', async () => {
    vi.spyOn(apiClient, 'get').mockResolvedValue({ data: null })
    expect(await shareSchedulingAPI.listGroups()).toEqual([])
    expect(await shareSchedulingAPI.accountRules(12)).toEqual([])
  })
})
