import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getActivePinia } from 'pinia'
import type { MeAccess } from '@/types/adminAccess'

const { getMeAccessMock, currentRoute, replaceMock } = vi.hoisted(() => ({
  getMeAccessMock: vi.fn(),
  currentRoute: {
    value: { path: '/admin/dashboard', meta: {} as Record<string, unknown> }
  },
  replaceMock: vi.fn()
}))

vi.mock('@/api/admin/access', () => ({
  getMeAccess: getMeAccessMock
}))

vi.mock('@/router', () => ({
  default: {
    currentRoute,
    replace: replaceMock
  }
}))

import {
  ADMIN_ACCESS_SUPER_CACHE_KEY,
  ADMIN_PERMISSION_DENIED_EVENT,
  PERMISSION_DENIED_REFRESH_INTERVAL_MS,
  useAdminAccessStore
} from '@/stores/adminAccess'

function seedAuth(role: 'admin' | 'operator' | 'user', runMode = 'standard') {
  getActivePinia()!.state.value.auth = { user: { id: 7, role }, runMode }
}

function meAccess(partial: Partial<MeAccess>): MeAccess {
  return {
    role: 'operator',
    is_super_admin: false,
    modules: {},
    flags: [],
    custom_menu_items: [],
    ...partial
  } as MeAccess
}

describe('adminAccess store', () => {
  beforeEach(() => {
    getMeAccessMock.mockReset()
    replaceMock.mockReset()
    replaceMock.mockResolvedValue(undefined)
    currentRoute.value = { path: '/admin/dashboard', meta: {} }
    localStorage.clear()
    vi.spyOn(console, 'warn').mockImplementation(() => {})
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('grants view through manage and checks flags', async () => {
    seedAuth('operator')
    getMeAccessMock.mockResolvedValue(
      meAccess({
        modules: { users: 'manage', accounts: 'view' },
        flags: ['users.balance_adjust']
      })
    )
    const store = useAdminAccessStore()
    await store.ensureLoaded()

    expect(store.can('users:view')).toBe(true)
    expect(store.can('users:manage')).toBe(true)
    expect(store.can('accounts:view')).toBe(true)
    expect(store.can('accounts:manage')).toBe(false)
    expect(store.can('groups:view')).toBe(false)
    expect(store.can('users.balance_adjust')).toBe(true)
    expect(store.can('data.export')).toBe(false)
    expect(localStorage.getItem(ADMIN_ACCESS_SUPER_CACHE_KEY)).toBe('false')
  })

  it('grants a super admin everything without waiting for me/access', () => {
    seedAuth('admin')
    getMeAccessMock.mockReturnValue(new Promise(() => {}))
    const store = useAdminAccessStore()

    expect(store.can('audit_logs:manage')).toBe(true)
    expect(store.can('orders:manage')).toBe(true)
    expect(store.can('data.export')).toBe(true)
    expect(store.adminHomePath).toBe('/admin/dashboard')
    expect(getMeAccessMock).not.toHaveBeenCalled()
  })

  it('fails closed for an operator until me/access has loaded', async () => {
    seedAuth('operator')
    let resolveAccess!: (value: MeAccess) => void
    getMeAccessMock.mockReturnValue(
      new Promise<MeAccess>((resolve) => {
        resolveAccess = resolve
      })
    )
    const store = useAdminAccessStore()
    const pending = store.ensureLoaded()

    expect(store.loaded).toBe(false)
    expect(store.can('dashboard:view')).toBe(false)
    expect(store.can('users.balance_adjust')).toBe(false)
    expect(store.adminHomePath).toBe('/profile')

    resolveAccess(meAccess({ modules: { dashboard: 'view' }, flags: ['data.export'] }))
    await pending

    expect(store.loaded).toBe(true)
    expect(store.can('dashboard:view')).toBe(true)
    expect(store.can('data.export')).toBe(true)
    expect(store.adminHomePath).toBe('/admin/dashboard')
  })

  it('stays fail-closed for an operator when me/access fails', async () => {
    seedAuth('operator')
    getMeAccessMock.mockRejectedValue(new Error('network'))
    const store = useAdminAccessStore()
    await store.ensureLoaded()

    expect(store.loaded).toBe(false)
    expect(store.can('dashboard:view')).toBe(false)
  })

  it('makes no request for a plain user', async () => {
    seedAuth('user')
    const store = useAdminAccessStore()
    await store.ensureLoaded()
    await store.refresh()
    await store.onPermissionDenied()

    expect(getMeAccessMock).not.toHaveBeenCalled()
    expect(store.can('dashboard:view')).toBe(false)
    expect(store.can('data.export')).toBe(false)
    expect(store.landingPath).toBe('/dashboard')
  })

  it('refreshes me/access at most once a minute after permission errors', async () => {
    let now = 1_000_000
    vi.spyOn(Date, 'now').mockImplementation(() => now)
    seedAuth('operator')
    getMeAccessMock.mockResolvedValueOnce(meAccess({ modules: { users: 'view', usage: 'view' } }))
    const store = useAdminAccessStore()
    await store.ensureLoaded()
    expect(getMeAccessMock).toHaveBeenCalledTimes(1)

    currentRoute.value = { path: '/admin/users', meta: { requiresAdmin: true, permission: 'users' } }
    getMeAccessMock.mockResolvedValue(meAccess({ modules: { usage: 'view' } }))

    // The window event starts the refresh; a second call joins the same run.
    window.dispatchEvent(new CustomEvent(ADMIN_PERMISSION_DENIED_EVENT))
    await store.onPermissionDenied()
    expect(getMeAccessMock).toHaveBeenCalledTimes(2)
    expect(store.can('users:view')).toBe(false)
    expect(replaceMock).toHaveBeenCalledTimes(1)
    expect(replaceMock).toHaveBeenCalledWith('/admin/usage')

    now += PERMISSION_DENIED_REFRESH_INTERVAL_MS - 1
    window.dispatchEvent(new CustomEvent(ADMIN_PERMISSION_DENIED_EVENT))
    await store.onPermissionDenied()
    expect(getMeAccessMock).toHaveBeenCalledTimes(2)

    currentRoute.value = { path: '/admin/usage', meta: { requiresAdmin: true, permission: 'usage' } }
    now += 1
    await store.onPermissionDenied()
    expect(getMeAccessMock).toHaveBeenCalledTimes(3)
    // The usage page is still permitted: no second redirect.
    expect(replaceMock).toHaveBeenCalledTimes(1)
  })

  it('computes the operator home from the first permitted admin page', async () => {
    seedAuth('operator')
    getMeAccessMock.mockResolvedValue(
      meAccess({
        modules: { ops: 'view', users: 'view', usage: 'view' },
        settings: { ops_monitoring_enabled: false } as MeAccess['settings']
      })
    )
    const store = useAdminAccessStore()
    await store.ensureLoaded()

    // Ops is off, so the first permitted page is users.
    expect(store.adminHomePath).toBe('/admin/users')
    expect(store.landingPath).toBe('/admin/users')

    // Users is hidden in simple mode: the next permitted page is usage.
    seedAuth('operator', 'simple')
    expect(store.adminHomePath).toBe('/admin/usage')
  })

  it('skips gated pages and falls back to the profile', async () => {
    seedAuth('operator')
    getActivePinia()!.state.value.app = { cachedPublicSettings: { payment_enabled: false } }
    getMeAccessMock.mockResolvedValue(
      meAccess({
        modules: { orders: 'manage', ops: 'view' },
        settings: { ops_monitoring_enabled: true, payment_enabled: true } as MeAccess['settings']
      })
    )
    const store = useAdminAccessStore()
    await store.ensureLoaded()
    expect(store.adminHomePath).toBe('/admin/ops')

    getMeAccessMock.mockResolvedValue(
      meAccess({ modules: { orders: 'manage' }, settings: { payment_enabled: true } as MeAccess['settings'] })
    )
    await store.refresh()
    expect(store.adminHomePath).toBe('/profile')
  })
})
