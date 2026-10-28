import { defineStore } from 'pinia'
import { ref } from 'vue'
import { adminAPI } from '@/api'
import type { CustomMenuItem } from '@/types'
import { useAuthStore } from './auth'
import { useAdminAccessStore } from './adminAccess'

export const useAdminSettingsStore = defineStore('adminSettings', () => {
  // A late settings read must not overwrite a more recent successful save.
  let usageMetricsRevision = 0
  const usageCacheHitRateEnabled = ref(false)
  const usageTokenSpeedEnabled = ref(false)

  function setUsageMetricsLocal(settings: { admin_usage_cache_hit_rate_enabled?: boolean; admin_usage_token_speed_enabled?: boolean }) {
    usageMetricsRevision++
    usageCacheHitRateEnabled.value = settings.admin_usage_cache_hit_rate_enabled ?? true
    usageTokenSpeedEnabled.value = settings.admin_usage_token_speed_enabled ?? true
  }

  const loaded = ref(false)
  const loading = ref(false)

  const readCachedBool = (key: string, defaultValue: boolean): boolean => {
    try {
      const raw = localStorage.getItem(key)
      if (raw === 'true') return true
      if (raw === 'false') return false
    } catch {
      // ignore localStorage failures
    }
    return defaultValue
  }

  const writeCachedBool = (key: string, value: boolean) => {
    try {
      localStorage.setItem(key, value ? 'true' : 'false')
    } catch {
      // ignore localStorage failures
    }
  }

  const readCachedString = (key: string, defaultValue: string): string => {
    try {
      const raw = localStorage.getItem(key)
      if (typeof raw === 'string' && raw.length > 0) return raw
    } catch {
      // ignore localStorage failures
    }
    return defaultValue
  }

  const writeCachedString = (key: string, value: string) => {
    try {
      localStorage.setItem(key, value)
    } catch {
      // ignore localStorage failures
    }
  }

  // Default open, but honor cached value to reduce UI flicker on first paint.
  const opsMonitoringEnabled = ref(readCachedBool('ops_monitoring_enabled_cached', true))
  const opsRealtimeMonitoringEnabled = ref(readCachedBool('ops_realtime_monitoring_enabled_cached', true))
  const opsQueryModeDefault = ref(readCachedString('ops_query_mode_default_cached', 'auto'))
  const paymentEnabled = ref(readCachedBool('payment_enabled_cached', false))
  const customMenuItems = ref<CustomMenuItem[]>([])

  // 侧栏、运维页、用量页会在同一时刻各自调用 fetch：共享进行中的请求，
  // 让后到的调用方也能等到数据，而不是立即返回默认值或重复请求。
  let inflight: Promise<void> | null = null

  function fetch(force = false): Promise<void> {
    if (loaded.value && !force) return Promise.resolve()
    if (inflight) {
      // 强制刷新（例如保存设置后）不能复用保存前发出的请求
      return force ? inflight.then(() => fetch(true)) : inflight
    }
    inflight = load().finally(() => {
      inflight = null
    })
    return inflight
  }

  // Operators cannot read /admin/settings or /admin/payment/config; the same
  // switches come from /admin/me/access.
  async function loadForOperator(): Promise<void> {
    loading.value = true
    const usageMetricsReadRevision = usageMetricsRevision
    try {
      await useAdminAccessStore().refresh()
      const access = useAdminAccessStore()
      const settings = access.settings
      if (!access.loaded || !settings) return
      if (usageMetricsReadRevision === usageMetricsRevision) setUsageMetricsLocal(settings)
      opsMonitoringEnabled.value = settings.ops_monitoring_enabled
      writeCachedBool('ops_monitoring_enabled_cached', opsMonitoringEnabled.value)

      opsRealtimeMonitoringEnabled.value = settings.ops_realtime_monitoring_enabled
      writeCachedBool('ops_realtime_monitoring_enabled_cached', opsRealtimeMonitoringEnabled.value)

      opsQueryModeDefault.value = settings.ops_query_mode_default || 'auto'
      writeCachedString('ops_query_mode_default_cached', opsQueryModeDefault.value)

      paymentEnabled.value = settings.payment_enabled
      writeCachedBool('payment_enabled_cached', paymentEnabled.value)

      customMenuItems.value = []
      loaded.value = true
    } catch (err) {
      console.error('[adminSettings] Failed to fetch operator settings:', err)
    } finally {
      loading.value = false
    }
  }

  async function load(): Promise<void> {
    const authStore = useAuthStore()
    if (!authStore.isAdmin && authStore.canAccessAdmin) {
      return loadForOperator()
    }
    loading.value = true
    const usageMetricsReadRevision = usageMetricsRevision
    try {
      const [settingsResult, paymentResult] = await Promise.allSettled([
        adminAPI.settings.getSettings(),
        adminAPI.payment.getConfig()
      ])
      if (settingsResult.status === 'rejected') throw settingsResult.reason
      const settings = settingsResult.value
      if (usageMetricsReadRevision === usageMetricsRevision) setUsageMetricsLocal(settings)
      opsMonitoringEnabled.value = settings.ops_monitoring_enabled ?? true
      writeCachedBool('ops_monitoring_enabled_cached', opsMonitoringEnabled.value)

      opsRealtimeMonitoringEnabled.value = settings.ops_realtime_monitoring_enabled ?? true
      writeCachedBool('ops_realtime_monitoring_enabled_cached', opsRealtimeMonitoringEnabled.value)

      opsQueryModeDefault.value = settings.ops_query_mode_default || 'auto'
      writeCachedString('ops_query_mode_default_cached', opsQueryModeDefault.value)

      customMenuItems.value = Array.isArray(settings.custom_menu_items) ? settings.custom_menu_items : []

      if (paymentResult.status === 'fulfilled') {
        paymentEnabled.value = paymentResult.value.data?.enabled ?? false
        writeCachedBool('payment_enabled_cached', paymentEnabled.value)
      }

      loaded.value = paymentResult.status === 'fulfilled'
    } catch (err) {
      // Keep cached/default value: do not "flip" the UI based on a transient fetch failure.
      console.error('[adminSettings] Failed to fetch settings:', err)
    } finally {
      loading.value = false
    }
  }

  function setOpsMonitoringEnabledLocal(value: boolean) {
    opsMonitoringEnabled.value = value
    writeCachedBool('ops_monitoring_enabled_cached', value)
    loaded.value = true
  }

  function setOpsRealtimeMonitoringEnabledLocal(value: boolean) {
    opsRealtimeMonitoringEnabled.value = value
    writeCachedBool('ops_realtime_monitoring_enabled_cached', value)
    loaded.value = true
  }

  function setPaymentEnabledLocal(value: boolean) {
    paymentEnabled.value = value
    writeCachedBool('payment_enabled_cached', value)
    loaded.value = true
  }

  function setOpsQueryModeDefaultLocal(value: string) {
    opsQueryModeDefault.value = value || 'auto'
    writeCachedString('ops_query_mode_default_cached', opsQueryModeDefault.value)
    loaded.value = true
  }

  // Keep UI consistent if we learn that ops is disabled via feature-gated 404s.
  // (event is dispatched from the axios interceptor)
  let eventHandlerCleanup: (() => void) | null = null

  function initializeEventListeners() {
    if (eventHandlerCleanup) return

    try {
      const handler = () => {
        setOpsMonitoringEnabledLocal(false)
      }
      window.addEventListener('ops-monitoring-disabled', handler)
      eventHandlerCleanup = () => {
        window.removeEventListener('ops-monitoring-disabled', handler)
      }
    } catch {
      // ignore window access failures (SSR)
    }
  }

  if (typeof window !== 'undefined') {
    initializeEventListeners()
  }

  return {
    usageCacheHitRateEnabled,
    usageTokenSpeedEnabled,
    setUsageMetricsLocal,
    loaded,
    loading,
    opsMonitoringEnabled,
    opsRealtimeMonitoringEnabled,
    opsQueryModeDefault,
    paymentEnabled,
    customMenuItems,
    fetch,
    setOpsMonitoringEnabledLocal,
    setOpsRealtimeMonitoringEnabledLocal,
    setPaymentEnabledLocal,
    setOpsQueryModeDefaultLocal
  }
})
