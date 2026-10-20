import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAdminSettingsStore } from '../adminSettings'

const mocks = vi.hoisted(() => ({ getSettings: vi.fn(), getConfig: vi.fn() }))
vi.mock('@/api', () => ({ adminAPI: { settings: { getSettings: mocks.getSettings }, payment: { getConfig: mocks.getConfig } } }))
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setActivePinia(createPinia())
  mocks.getSettings.mockResolvedValue({ ops_monitoring_enabled: true, custom_menu_items: [{ id: 'custom', title: 'Custom' }] })
  mocks.getConfig.mockResolvedValue({ data: { enabled: true } })
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => { vi.restoreAllMocks(); localStorage.clear() })

describe('admin settings fetch retry', () => {
  it.each(['settings', 'payment'])('retries a failed initial %s request without forcing a refresh', async (source) => {
    const request = source === 'settings' ? mocks.getSettings : mocks.getConfig
    request.mockRejectedValueOnce(new Error('Temporarily unavailable'))
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.loaded).toBe(false)
    expect(store.loading).toBe(false)
    await store.fetch()
    expect(mocks.getSettings).toHaveBeenCalledTimes(2)
    expect(mocks.getConfig).toHaveBeenCalledTimes(2)
    expect(store.loaded).toBe(true)
    expect(store.paymentEnabled).toBe(true)
    expect(store.customMenuItems).toEqual([{ id: 'custom', title: 'Custom' }])
  })

  it('keeps cached values visible when the initial request fails', async () => {
    localStorage.setItem('ops_monitoring_enabled_cached', 'false')
    localStorage.setItem('payment_enabled_cached', 'true')
    mocks.getSettings.mockRejectedValueOnce(new Error('Offline'))
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.opsMonitoringEnabled).toBe(false)
    expect(store.paymentEnabled).toBe(true)
    expect(localStorage.getItem('payment_enabled_cached')).toBe('true')
  })

  it('still reuses successfully loaded settings', async () => {
    const store = useAdminSettingsStore()
    await store.fetch()
    await store.fetch()
    expect(mocks.getSettings).toHaveBeenCalledTimes(1)
    expect(mocks.getConfig).toHaveBeenCalledTimes(1)
  })

  it('lets concurrent callers share one in-flight request and wait for its data', async () => {
    const store = useAdminSettingsStore()
    const first = store.fetch()
    const second = store.fetch()
    await second
    expect(store.loaded).toBe(true)
    expect(store.customMenuItems).toEqual([{ id: 'custom', title: 'Custom' }])
    await first
    expect(mocks.getSettings).toHaveBeenCalledTimes(1)
  })

  it('refetches after the in-flight request when a forced refresh arrives mid-request', async () => {
    const store = useAdminSettingsStore()
    const first = store.fetch()
    mocks.getSettings.mockResolvedValueOnce({ custom_menu_items: [{ id: 'saved', title: 'Saved' }] })
    await store.fetch(true)
    await first
    expect(mocks.getSettings).toHaveBeenCalledTimes(2)
    expect(store.customMenuItems).toEqual([{ id: 'saved', title: 'Saved' }])
  })
})
