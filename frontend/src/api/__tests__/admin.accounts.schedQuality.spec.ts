import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import { exportData, list } from '../admin/accounts'

afterEach(() => vi.restoreAllMocks())

describe('account export filters', () => {
  it('sends the cooling filter in the backend 0.6.5 word (substandard); an old demote link filters nothing', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: {} })
    await exportData({ filters: { platform: 'openai', sched_quality: 'cooling' } })
    await exportData({ filters: { sched_quality: '' } })
    await exportData({ filters: { sched_quality: 'demote' } })
    await exportData({ filters: { sched_quality: 'observing' } })
    expect(get).toHaveBeenNthCalledWith(1, '/admin/accounts/data', { params: { platform: 'openai', sched_quality: 'substandard' } })
    expect(get).toHaveBeenNthCalledWith(2, '/admin/accounts/data', { params: {} })
    expect(get).toHaveBeenNthCalledWith(3, '/admin/accounts/data', { params: {} })
    expect(get).toHaveBeenNthCalledWith(4, '/admin/accounts/data', { params: { sched_quality: 'observing' } })
  })

  it('translates the quality filter of the list request', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20, pages: 0 } })
    await list(1, 20, { sched_quality: 'cooling', platform: 'openai' } as never)
    expect(get.mock.calls[0][1]).toMatchObject({ params: { page: 1, page_size: 20, platform: 'openai', sched_quality: 'substandard' } })
  })
})
