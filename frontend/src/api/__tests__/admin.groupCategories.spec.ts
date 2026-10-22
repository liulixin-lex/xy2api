import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import groupCategoriesAPI from '../admin/groupCategories'
import { exportData, list as listAccounts } from '../admin/accounts'
import { list as listGroups } from '../admin/groups'

afterEach(() => vi.restoreAllMocks())

describe('group categories admin API', () => {
  it('uses the group-categories endpoints and payloads', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: [{ id: 1, name: 'Public', sort_order: 0, group_ids: [3] }] })
    const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: { group_id: 7, category_ids: [1, 3] } })

    expect(await groupCategoriesAPI.list()).toEqual([{ id: 1, name: 'Public', sort_order: 0, group_ids: [3] }])
    await groupCategoriesAPI.save({ items: [{ id: 4, name: 'Monitor' }, { name: 'Dedicated' }], deleted_ids: [2] })
    expect(await groupCategoriesAPI.setGroupCategories(7, [1, 3])).toEqual({ group_id: 7, category_ids: [1, 3] })

    expect(get).toHaveBeenCalledWith('/admin/group-categories')
    expect(put).toHaveBeenNthCalledWith(1, '/admin/group-categories', {
      items: [{ id: 4, name: 'Monitor' }, { name: 'Dedicated' }],
      deleted_ids: [2]
    })
    expect(put).toHaveBeenNthCalledWith(2, '/admin/group-categories/groups/7', { category_ids: [1, 3] })
  })

  it('returns empty lists for empty responses', async () => {
    vi.spyOn(apiClient, 'get').mockResolvedValue({ data: null })
    vi.spyOn(apiClient, 'put').mockResolvedValue({ data: null })
    expect(await groupCategoriesAPI.list()).toEqual([])
    expect(await groupCategoriesAPI.save({ items: [], deleted_ids: [] })).toEqual([])
  })

  it('passes the group_category filter to the group list, account list and export', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: { items: [], total: 0 } })

    await listGroups(1, 20, { group_category: '3' })
    await listAccounts(1, 20, { group_category: '3' })
    await exportData({ filters: { group: '5', group_category: '3' } })
    await exportData({ filters: { group_category: '' } })

    expect(get).toHaveBeenNthCalledWith(1, '/admin/groups', expect.objectContaining({ params: { page: 1, page_size: 20, group_category: '3' } }))
    expect(get).toHaveBeenNthCalledWith(2, '/admin/accounts', expect.objectContaining({ params: { page: 1, page_size: 20, group_category: '3' } }))
    expect(get).toHaveBeenNthCalledWith(3, '/admin/accounts/data', { params: { group: '5', group_category: '3' } })
    expect(get).toHaveBeenNthCalledWith(4, '/admin/accounts/data', { params: {} })
  })
})
