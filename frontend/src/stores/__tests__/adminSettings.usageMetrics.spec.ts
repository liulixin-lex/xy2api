import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAdminSettingsStore } from '../adminSettings'

const { getSettings, getConfig } = vi.hoisted(() => ({ getSettings: vi.fn(), getConfig: vi.fn() }))
vi.mock('@/api', () => ({ adminAPI: { settings: { getSettings }, payment: { getConfig } } }))

beforeEach(() => {
  setActivePinia(createPinia())
  getSettings.mockReset().mockResolvedValue({})
  getConfig.mockReset().mockResolvedValue({ data: { enabled: false } })
})

describe('admin usage display settings', () => {
  it('waits for settings and defaults missing flags to enabled', async () => {
    const store = useAdminSettingsStore()
    expect(store.usageCacheHitRateEnabled).toBe(false)
    expect(store.usageTokenSpeedEnabled).toBe(false)
    await store.fetch()
    expect(store.usageCacheHitRateEnabled).toBe(true)
    expect(store.usageTokenSpeedEnabled).toBe(true)
  })
  it.each([[true,true],[true,false],[false,true],[false,false]])('loads and refreshes independent flags %s / %s', async (cache, speed) => {
    getSettings.mockResolvedValue({ admin_usage_cache_hit_rate_enabled: cache, admin_usage_token_speed_enabled: speed })
    const store = useAdminSettingsStore()
    await store.fetch()
    expect([store.usageCacheHitRateEnabled, store.usageTokenSpeedEnabled]).toEqual([cache, speed])
    store.setUsageMetricsLocal({ admin_usage_cache_hit_rate_enabled: !cache, admin_usage_token_speed_enabled: !speed })
    expect([store.usageCacheHitRateEnabled, store.usageTokenSpeedEnabled]).toEqual([!cache, !speed])
    await store.fetch(true)
    expect([store.usageCacheHitRateEnabled, store.usageTokenSpeedEnabled]).toEqual([cache, speed])
    setActivePinia(createPinia())
    const reloaded = useAdminSettingsStore()
    await reloaded.fetch()
    expect([reloaded.usageCacheHitRateEnabled, reloaded.usageTokenSpeedEnabled]).toEqual([cache, speed])
  })
  it('loads display flags even if the unrelated payment request fails', async () => {
    getConfig.mockRejectedValue(new Error('payment unavailable'))
    await useAdminSettingsStore().fetch()
    expect(useAdminSettingsStore().usageCacheHitRateEnabled).toBe(true)
  })
  it('does not overwrite a successful save with a late settings read', async () => {
    let resolveSettings!: (value: object) => void
    getSettings.mockReturnValue(new Promise(resolve => { resolveSettings = resolve }))
    const store = useAdminSettingsStore()
    const pending = store.fetch()
    store.setUsageMetricsLocal({ admin_usage_cache_hit_rate_enabled: false, admin_usage_token_speed_enabled: false })
    resolveSettings({ admin_usage_cache_hit_rate_enabled: true, admin_usage_token_speed_enabled: true })
    await pending
    expect([store.usageCacheHitRateEnabled, store.usageTokenSpeedEnabled]).toEqual([false, false])
  })

  it('preserves last confirmed flags when settings fail', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const store = useAdminSettingsStore()
    store.setUsageMetricsLocal({ admin_usage_cache_hit_rate_enabled: false, admin_usage_token_speed_enabled: true })
    getSettings.mockRejectedValue(new Error('settings unavailable'))
    await store.fetch(true)
    expect([store.usageCacheHitRateEnabled, store.usageTokenSpeedEnabled]).toEqual([false, true])
    error.mockRestore()
  })
})
