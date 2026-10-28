import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get }
}))

import { accountsAPI, getUpstreamBalances } from '@/api/admin/accounts'

describe('admin account upstream balances API', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('reads cached upstream balances for the given ids with one GET', async () => {
    const payload = {
      items: [
        {
          account_id: 1,
          state: 'ok',
          amount_usd: 12.34,
          unlimited: false,
          basis: 'wallet',
          upstream_kind: 'sub2api',
          reason: null,
          observed_at: '2026-10-27T12:00:00Z',
          last_attempt_at: '2026-10-27T12:01:00Z'
        }
      ],
      server_time: '2026-10-27T12:01:05Z'
    }
    get.mockResolvedValueOnce({ data: payload })
    const controller = new AbortController()

    const result = await getUpstreamBalances([1, 2, 3], { signal: controller.signal })

    expect(get).toHaveBeenCalledTimes(1)
    expect(get).toHaveBeenCalledWith('/admin/accounts/upstream-balances', {
      params: { ids: '1,2,3' },
      signal: controller.signal
    })
    expect(result).toEqual(payload)
    expect(result.server_time).toBe('2026-10-27T12:01:05Z')
    expect(accountsAPI.getUpstreamBalances).toBe(getUpstreamBalances)
  })

  it('sends no signal when the caller does not pass one', async () => {
    get.mockResolvedValueOnce({ data: { items: [] } })

    await expect(getUpstreamBalances([7])).resolves.toEqual({ items: [] })
    expect(get).toHaveBeenCalledWith('/admin/accounts/upstream-balances', {
      params: { ids: '7' },
      signal: undefined
    })
  })
})
