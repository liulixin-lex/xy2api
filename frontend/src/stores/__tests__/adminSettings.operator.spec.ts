import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getActivePinia } from 'pinia'
import { useAdminSettingsStore } from '../adminSettings'

const mocks = vi.hoisted(() => ({ getSettings: vi.fn(), getConfig: vi.fn(), getMeAccess: vi.fn() }))

vi.mock('@/api', () => ({
  adminAPI: { settings: { getSettings: mocks.getSettings }, payment: { getConfig: mocks.getConfig } }
}))
vi.mock('@/api/admin/access', () => ({ getMeAccess: mocks.getMeAccess }))
vi.mock('@/stores/auth', async () => {
  const { getActivePinia } = await import('pinia')
  const role = () => (getActivePinia()!.state.value.auth?.user as { role?: string } | null | undefined)?.role
  return {
    useAuthStore: () => ({
      get isAdmin() { return role() === 'admin' },
      get canAccessAdmin() { return role() === 'admin' || role() === 'operator' }
    })
  }
})

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  getActivePinia()!.state.value.auth = { user: { id: 3, role: 'operator' }, runMode: 'standard' }
  mocks.getMeAccess.mockResolvedValue({
    role: 'operator',
    is_super_admin: false,
    modules: { ops: 'view', usage: 'view' },
    flags: [],
    settings: {
      ops_monitoring_enabled: false,
      ops_realtime_monitoring_enabled: true,
      ops_query_mode_default: 'raw',
      admin_usage_cache_hit_rate_enabled: false,
      admin_usage_token_speed_enabled: true,
      payment_enabled: true
    },
    custom_menu_items: [
      { id: 'admin-only', label: 'Admin Only', icon_svg: '', url: 'https://example.com', visibility: 'admin', sort_order: 1 }
    ]
  })
})
afterEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
})

describe('admin settings for operators', () => {
  it('loads operator settings from me/access without calling admin settings or payment config', async () => {
    localStorage.setItem('ops_monitoring_enabled_cached', 'true')
    localStorage.setItem('payment_enabled_cached', 'false')
    const store = useAdminSettingsStore()

    await store.fetch()

    expect(mocks.getSettings).not.toHaveBeenCalled()
    expect(mocks.getConfig).not.toHaveBeenCalled()
    expect(mocks.getMeAccess).toHaveBeenCalledTimes(1)

    expect(store.loaded).toBe(true)
    expect(store.loading).toBe(false)
    expect(store.opsMonitoringEnabled).toBe(false)
    expect(store.opsRealtimeMonitoringEnabled).toBe(true)
    expect(store.opsQueryModeDefault).toBe('raw')
    expect(store.paymentEnabled).toBe(true)
    expect(store.usageCacheHitRateEnabled).toBe(false)
    expect(store.usageTokenSpeedEnabled).toBe(true)
    // Custom admin menu items are for super admins only.
    expect(store.customMenuItems).toEqual([])

    expect(localStorage.getItem('ops_monitoring_enabled_cached')).toBe('false')
    expect(localStorage.getItem('ops_realtime_monitoring_enabled_cached')).toBe('true')
    expect(localStorage.getItem('ops_query_mode_default_cached')).toBe('raw')
    expect(localStorage.getItem('payment_enabled_cached')).toBe('true')

    // Loaded settings are reused.
    await store.fetch()
    expect(mocks.getMeAccess).toHaveBeenCalledTimes(1)
  })

  it('stays unloaded and keeps cached values when me/access fails', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    localStorage.setItem('payment_enabled_cached', 'true')
    mocks.getMeAccess.mockRejectedValueOnce(new Error('offline'))
    const store = useAdminSettingsStore()

    await store.fetch()

    expect(store.loaded).toBe(false)
    expect(store.paymentEnabled).toBe(true)
    expect(mocks.getSettings).not.toHaveBeenCalled()
    expect(mocks.getConfig).not.toHaveBeenCalled()

    await store.fetch()
    expect(mocks.getMeAccess).toHaveBeenCalledTimes(2)
    expect(store.loaded).toBe(true)
  })

  it('reloads full admin settings when a super admin signs in after an operator session', async () => {
    const adminMenu = [{ id: 'm1', label: 'Docs', icon_svg: '', url: 'https://example.com', visibility: 'admin', sort_order: 1 }]
    mocks.getSettings.mockResolvedValue({ ops_monitoring_enabled: true, custom_menu_items: adminMenu })
    mocks.getConfig.mockResolvedValue({ data: { enabled: false } })
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.loaded).toBe(true)
    expect(mocks.getSettings).not.toHaveBeenCalled()

    getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }
    expect(store.loaded).toBe(false)

    await store.fetch()
    expect(mocks.getSettings).toHaveBeenCalledTimes(1)
    expect(mocks.getConfig).toHaveBeenCalledTimes(1)
    expect(store.loaded).toBe(true)
    expect(store.customMenuItems).toEqual(adminMenu)
    expect(store.paymentEnabled).toBe(false)
  })

  it('drops an operator load that finishes after the user changed', async () => {
    let resolveAccess!: (value: unknown) => void
    mocks.getMeAccess.mockReturnValueOnce(new Promise((resolve) => { resolveAccess = resolve }))
    mocks.getSettings.mockResolvedValue({ ops_monitoring_enabled: true, custom_menu_items: [] })
    mocks.getConfig.mockResolvedValue({ data: { enabled: true } })
    const store = useAdminSettingsStore()
    const stale = store.fetch()

    getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }
    resolveAccess({ role: 'operator', is_super_admin: false, modules: {}, flags: [], settings: { payment_enabled: false }, custom_menu_items: [] })
    await stale
    expect(store.loaded).toBe(false)

    await store.fetch()
    expect(mocks.getSettings).toHaveBeenCalledTimes(1)
    expect(store.loaded).toBe(true)
    expect(store.paymentEnabled).toBe(true)
  })
})
