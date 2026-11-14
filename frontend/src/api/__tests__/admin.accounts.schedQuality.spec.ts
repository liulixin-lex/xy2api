import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import { exportData } from '../admin/accounts'

afterEach(() => vi.restoreAllMocks())

describe('account export filters', () => {
  it('passes the substandard filter on', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: {} })
    await exportData({ filters: { platform: 'openai', sched_quality: 'substandard' } })
    await exportData({ filters: { sched_quality: '' } })
    expect(get).toHaveBeenNthCalledWith(1, '/admin/accounts/data', { params: { platform: 'openai', sched_quality: 'substandard' } })
    expect(get).toHaveBeenNthCalledWith(2, '/admin/accounts/data', { params: {} })
  })
})
