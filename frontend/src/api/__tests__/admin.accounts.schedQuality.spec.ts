import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import { exportData } from '../admin/accounts'

afterEach(() => vi.restoreAllMocks())

describe('account export filters', () => {
  it('passes the cooling filter on (contract clarifications K11 / K24)', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: {} })
    await exportData({ filters: { platform: 'openai', sched_quality: 'cooling' } })
    await exportData({ filters: { sched_quality: '' } })
    expect(get).toHaveBeenNthCalledWith(1, '/admin/accounts/data', { params: { platform: 'openai', sched_quality: 'cooling' } })
    expect(get).toHaveBeenNthCalledWith(2, '/admin/accounts/data', { params: {} })
  })
})
