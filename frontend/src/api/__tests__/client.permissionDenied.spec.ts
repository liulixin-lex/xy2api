import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AxiosInstance } from 'axios'
import { ADMIN_PERMISSION_DENIED_EVENT } from '@/stores/adminAccess'

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN'
}))

function rejectWith(status: number, data: Record<string, unknown>, url: string) {
  return vi.fn().mockRejectedValue({
    response: { status, data, headers: {} },
    config: { url, headers: {} },
    code: 'ERR_BAD_REQUEST'
  })
}

describe('API client permission-denied event', () => {
  let apiClient: AxiosInstance
  let events: CustomEvent[]
  const listener = (event: Event) => {
    events.push(event as CustomEvent)
  }

  beforeEach(async () => {
    localStorage.clear()
    vi.resetModules()
    apiClient = (await import('@/api/client')).apiClient
    events = []
    window.addEventListener(ADMIN_PERMISSION_DENIED_EVENT, listener)
  })

  afterEach(() => {
    window.removeEventListener(ADMIN_PERMISSION_DENIED_EVENT, listener)
    vi.restoreAllMocks()
  })

  it('emits one event for ADMIN_PERMISSION_DENIED and SUPER_ADMIN_REQUIRED only', async () => {
    expect(ADMIN_PERMISSION_DENIED_EVENT).toBe('admin-permission-denied')

    apiClient.defaults.adapter = rejectWith(
      403,
      { code: 'FORBIDDEN', reason: 'ADMIN_PERMISSION_DENIED', message: 'denied', metadata: { required: 'users:manage' } },
      '/admin/users'
    )
    await expect(apiClient.get('/admin/users')).rejects.toMatchObject({
      status: 403,
      reason: 'ADMIN_PERMISSION_DENIED',
      metadata: { required: 'users:manage' }
    })
    expect(events).toHaveLength(1)
    expect(events[0].detail).toEqual({ reason: 'ADMIN_PERMISSION_DENIED', required: 'users:manage', url: '/admin/users' })

    apiClient.defaults.adapter = rejectWith(
      403,
      { code: 'FORBIDDEN', reason: 'SUPER_ADMIN_REQUIRED', message: 'super admin only' },
      '/admin/settings'
    )
    await expect(apiClient.get('/admin/settings')).rejects.toMatchObject({ status: 403, reason: 'SUPER_ADMIN_REQUIRED' })
    expect(events).toHaveLength(2)
    expect(events[1].detail).toEqual({ reason: 'SUPER_ADMIN_REQUIRED', required: undefined, url: '/admin/settings' })

    // Other 403 reasons and the same reasons on other statuses emit nothing.
    const silent: Array<[number, Record<string, unknown>]> = [
      [403, { code: 'FORBIDDEN', reason: 'ADMIN_FIELD_FORBIDDEN', metadata: { field: 'email' } }],
      [403, { code: 'FORBIDDEN', message: 'forbidden' }],
      [403, { code: 'ADMIN_PERMISSION_DENIED', message: 'code is not the reason' }],
      [404, { code: 'NOT_FOUND', reason: 'ADMIN_PERMISSION_DENIED' }],
      [500, { code: 'INTERNAL', reason: 'SUPER_ADMIN_REQUIRED' }]
    ]
    for (const [status, data] of silent) {
      apiClient.defaults.adapter = rejectWith(status, data, '/admin/accounts')
      await expect(apiClient.get('/admin/accounts')).rejects.toMatchObject({ status })
    }
    expect(events).toHaveLength(2)
  })
})
