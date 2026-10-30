/**
 * Admin access store (operator role, 普通管理员).
 *
 * Loads `GET /admin/me/access` for admins and operators and answers `can()`
 * questions for the router, the sidebar and the pages. The backend is the
 * security boundary; this store only keeps operators away from pages and
 * buttons that would 403.
 *
 * - A super admin can do everything; `can()` never waits for a request.
 * - A plain user can do nothing; no request is made.
 * - An operator can do nothing until `/me/access` has loaded (fail closed).
 *
 * The signed-in user, the run mode and the public settings are read from the
 * auth and app stores' state in Pinia (`pinia.state.value.auth` / `.app`, the
 * same refs `useAuthStore()` and `useAppStore()` expose). This module does not
 * import those stores: both pull in the HTTP client and i18n, and every admin
 * component that only needs `can()` would otherwise depend on them too. The
 * `/me/access` request and the router are imported lazily for the same reason.
 */
import { defineStore, getActivePinia } from 'pinia'
import { computed, ref, watch } from 'vue'
import type { PublicSettings, User } from '@/types'
import type {
  AdminFlagId,
  AdminModuleId,
  AdminPermission,
  MeAccess,
  MeAccessSettings
} from '@/types/adminAccess'

/** Read by `api/client.ts` without importing a store (C6). */
export const ADMIN_ACCESS_SUPER_CACHE_KEY = 'admin_access_is_super_cached'
/** Dispatched by `api/client.ts` on 403 ADMIN_PERMISSION_DENIED / SUPER_ADMIN_REQUIRED. */
export const ADMIN_PERMISSION_DENIED_EVENT = 'admin-permission-denied'
/** A permission error refreshes `/me/access` at most once per this interval. */
export const PERMISSION_DENIED_REFRESH_INTERVAL_MS = 60_000

type AdminPageGate = 'opsMonitoring' | 'payment' | 'channelMonitor' | 'subscription' | 'riskControl' | 'affiliate' | 'supportTickets'

/**
 * Public-settings switches that hide admin pages, with the value used while
 * public settings are not loaded. Mirrors `FeatureFlags` in
 * `utils/featureFlags.ts` (opt-out = true, opt-in = false); a spec keeps the
 * two in step.
 */
export const ADMIN_PAGE_PUBLIC_GATES = {
  channelMonitor: { key: 'channel_monitor_enabled', whenUnset: true },
  subscription: { key: 'subscription_enabled', whenUnset: true },
  riskControl: { key: 'risk_control_enabled', whenUnset: false },
  supportTickets: { key: 'support_ticket_enabled', whenUnset: false },
  affiliate: { key: 'affiliate_enabled', whenUnset: false }
} as const satisfies Record<string, { key: keyof PublicSettings; whenUnset: boolean }>

interface AuthStateView {
  user?: Pick<User, 'id' | 'role'> | null
  runMode?: string
}

interface AppStateView {
  cachedPublicSettings?: Partial<PublicSettings> | null
}

export interface AdminPageEntry {
  path: string
  permission: AdminModuleId | 'super_only'
  /** Feature switch that hides the page (same switch the sidebar uses). */
  gate?: AdminPageGate
  hideInSimpleMode?: boolean
}

/**
 * Admin pages in sidebar order (AppSidebar `adminNavItems`). The operator
 * home is the first entry whose permission and feature switch pass. The
 * router spec checks that every entry matches the route's `meta.permission`.
 */
export const ADMIN_PAGES_IN_SIDEBAR_ORDER: readonly AdminPageEntry[] = [
  { path: '/admin/dashboard', permission: 'dashboard' },
  { path: '/admin/ops', permission: 'ops', gate: 'opsMonitoring' },
  { path: '/admin/users', permission: 'users', hideInSimpleMode: true },
  { path: '/admin/groups', permission: 'groups' },
  { path: '/admin/channels/pricing', permission: 'channels', hideInSimpleMode: true },
  { path: '/admin/channels/monitor', permission: 'channels', gate: 'channelMonitor', hideInSimpleMode: true },
  { path: '/admin/subscriptions', permission: 'subscriptions', gate: 'subscription', hideInSimpleMode: true },
  { path: '/admin/accounts', permission: 'accounts' },
  { path: '/admin/share-scheduling', permission: 'share_scheduling' },
  { path: '/admin/plugins', permission: 'super_only' },
  { path: '/admin/announcements', permission: 'announcements' },
  { path: '/admin/support-tickets', permission: 'support_tickets', gate: 'supportTickets' },
  { path: '/admin/support-tickets/:id(\\d+)', permission: 'support_tickets', gate: 'supportTickets' },
  { path: '/admin/proxies', permission: 'proxies' },
  { path: '/admin/risk-control', permission: 'security_audit', gate: 'riskControl' },
  { path: '/admin/prompt-audit', permission: 'security_audit', gate: 'riskControl' },
  { path: '/admin/redeem', permission: 'redeem_codes', hideInSimpleMode: true },
  { path: '/admin/promo-codes', permission: 'promo_codes', hideInSimpleMode: true },
  { path: '/admin/affiliates/invites', permission: 'affiliates', gate: 'affiliate', hideInSimpleMode: true },
  { path: '/admin/affiliates/rebates', permission: 'affiliates', gate: 'affiliate', hideInSimpleMode: true },
  { path: '/admin/affiliates/transfers', permission: 'affiliates', gate: 'affiliate', hideInSimpleMode: true },
  { path: '/admin/orders/dashboard', permission: 'orders', gate: 'payment', hideInSimpleMode: true },
  { path: '/admin/orders', permission: 'orders', gate: 'payment', hideInSimpleMode: true },
  { path: '/admin/orders/plans', permission: 'orders', gate: 'payment', hideInSimpleMode: true },
  { path: '/admin/usage', permission: 'usage' },
  { path: '/admin/audit-logs', permission: 'audit_logs', hideInSimpleMode: true },
  { path: '/admin/settings', permission: 'super_only' }
]

const DEFAULT_SETTINGS: MeAccessSettings = {
  ops_monitoring_enabled: false,
  ops_realtime_monitoring_enabled: false,
  ops_query_mode_default: 'auto',
  admin_usage_cache_hit_rate_enabled: false,
  admin_usage_token_speed_enabled: false,
  payment_enabled: false,
  account_quota_notify_enabled: false,
  web_search_emulation_enabled: false,
  payment_subscription_usd_to_cny_rate: 0,
  payment_recharge_fee_rate: 0
}

function normalizeMeAccess(raw: MeAccess): MeAccess {
  const modules = raw && typeof raw.modules === 'object' && raw.modules !== null ? raw.modules : {}
  const flags = Array.isArray(raw?.flags) ? raw.flags : []
  const settings = { ...DEFAULT_SETTINGS, ...(raw?.settings ?? {}) }
  return {
    role: raw?.role === 'admin' ? 'admin' : 'operator',
    is_super_admin: raw?.is_super_admin === true,
    modules: { ...modules },
    flags: [...flags],
    settings,
    custom_menu_items: Array.isArray(raw?.custom_menu_items) ? raw.custom_menu_items : []
  }
}

function writeSuperCache(value: boolean | null) {
  try {
    if (value === null) {
      localStorage.removeItem(ADMIN_ACCESS_SUPER_CACHE_KEY)
    } else {
      localStorage.setItem(ADMIN_ACCESS_SUPER_CACHE_KEY, value ? 'true' : 'false')
    }
  } catch {
    // ignore localStorage failures
  }
}

// One window listener at a time, bound to the newest store instance.
let detachPermissionDeniedListener: (() => void) | null = null

export const useAdminAccessStore = defineStore('adminAccess', () => {
  const pinia = getActivePinia()
  const piniaState = () => (pinia?.state.value ?? {}) as Record<string, unknown>
  const authUser = computed(() => (piniaState().auth as AuthStateView | undefined)?.user ?? null)
  const role = computed(() => authUser.value?.role)
  /** Same predicates as `authStore.isAdmin`, `authStore.canAccessAdmin` and `authStore.isSimpleMode`. */
  const authStore = {
    get user() {
      return authUser.value
    },
    get isAdmin() {
      return role.value === 'admin'
    },
    get canAccessAdmin() {
      return role.value === 'admin' || role.value === 'operator'
    },
    get isSimpleMode() {
      return (piniaState().auth as AuthStateView | undefined)?.runMode === 'simple'
    }
  }
  const publicSettings = () => (piniaState().app as AppStateView | undefined)?.cachedPublicSettings ?? null

  const access = ref<MeAccess | null>(null)
  const loaded = ref(false)
  const loading = ref(false)
  /** Incremented on every successful load. */
  const version = ref(0)

  let inflight: Promise<void> | null = null
  // Bumped on reset so a response for a previous session is dropped.
  let generation = 0
  let lastDeniedRefreshAt = 0
  let deniedInflight: Promise<void> | null = null

  const isSuperAdmin = computed(() => authStore.isAdmin)
  /** The signed-in user is an operator: the per-field operator rules apply. */
  const isOperator = computed(() => role.value === 'operator')
  const settings = computed<MeAccessSettings | undefined>(() => access.value?.settings)

  function can(permission: AdminPermission): boolean {
    if (authStore.isAdmin) return true
    if (!authStore.canAccessAdmin) {
      // A signed-in plain user gets nothing. Without a signed-in user there is
      // no role to restrict: admin pages are unreachable then (the router and
      // the sidebar check canAccessAdmin first), and components rendered
      // outside the app shell keep their pre-operator behaviour.
      return !authStore.user
    }
    const current = access.value
    if (!loaded.value || !current) return false
    const sep = permission.indexOf(':')
    if (sep > 0) {
      const level = current.modules[permission.slice(0, sep) as AdminModuleId]
      const wanted = permission.slice(sep + 1)
      if (wanted === 'view') return level === 'view' || level === 'manage'
      if (wanted === 'manage') return level === 'manage'
      return false
    }
    return current.flags.includes(permission as AdminFlagId)
  }

  function gateOpen(gate: AdminPageGate): boolean {
    const settingsNow = publicSettings()
    if (gate === 'opsMonitoring') return access.value?.settings.ops_monitoring_enabled === true
    if (gate === 'payment') {
      return access.value?.settings.payment_enabled === true && settingsNow?.payment_enabled !== false
    }
    const { key, whenUnset } = ADMIN_PAGE_PUBLIC_GATES[gate]
    const raw = settingsNow?.[key]
    return typeof raw === 'boolean' ? raw : whenUnset
  }

  /**
   * Super admin: the dashboard. Operator: the first permitted admin page in
   * sidebar order, after feature switches; `/profile` when there is none.
   */
  const adminHomePath = computed(() => {
    if (authStore.isAdmin) return '/admin/dashboard'
    for (const page of ADMIN_PAGES_IN_SIDEBAR_ORDER) {
      if (page.permission === 'super_only') continue
      if (!can(`${page.permission}:view`)) continue
      if (page.hideInSimpleMode && authStore.isSimpleMode) continue
      if (page.gate && !gateOpen(page.gate)) continue
      return page.path
    }
    return '/profile'
  })

  /** Where "home" points for the current user: admin home, operator home or user dashboard. */
  const landingPath = computed(() => {
    if (authStore.isAdmin) return '/admin/dashboard'
    if (authStore.canAccessAdmin) return adminHomePath.value
    return '/dashboard'
  })

  function load(): Promise<void> {
    if (inflight) return inflight
    const loadGeneration = generation
    loading.value = true
    // Loaded lazily so pages that import this store do not pull in the HTTP client.
    const request = import('@/api/admin/access')
      .then(({ getMeAccess }) => getMeAccess())
      .then((data) => {
        if (loadGeneration !== generation) return
        const normalized = normalizeMeAccess(data)
        access.value = normalized
        loaded.value = true
        version.value += 1
        writeSuperCache(normalized.is_super_admin)
      })
      .catch((error) => {
        if (loadGeneration !== generation) return
        // Keep the previous answer (or none): an operator stays fail-closed.
        console.warn('[adminAccess] Failed to load /admin/me/access:', error)
      })
      .finally(() => {
        if (inflight === request) {
          inflight = null
          loading.value = false
        }
      })
    inflight = request
    return request
  }

  /** Loads `/me/access` once for admins and operators (single-flight). Plain users resolve at once. */
  function ensureLoaded(): Promise<void> {
    if (!authStore.canAccessAdmin) return Promise.resolve()
    if (loaded.value) return Promise.resolve()
    return load()
  }

  /** Forces a reload (single-flight). */
  function refresh(): Promise<void> {
    if (!authStore.canAccessAdmin) return Promise.resolve()
    return load()
  }

  function reset() {
    generation += 1
    inflight = null
    access.value = null
    loaded.value = false
    loading.value = false
    lastDeniedRefreshAt = 0
    deniedInflight = null
    writeSuperCache(null)
  }

  // ---------- 403 handling (C6) ----------

  async function redirectIfCurrentRouteDenied() {
    const { default: router } = await import('@/router')
    const current = router.currentRoute.value
    if (current.meta?.requiresAdmin !== true || authStore.isAdmin) return
    const permission = current.meta.permission
    if (permission && permission !== 'super_only' && can(`${permission}:view`)) return
    const home = adminHomePath.value
    if (home !== current.path) {
      await router.replace(home)
    }
  }

  /**
   * Called on every `admin-permission-denied` event. Refreshes `/me/access`
   * at most once per minute; when the refresh changed the access and the
   * current page is no longer permitted, moves to the admin home.
   */
  function onPermissionDenied(): Promise<void> {
    if (!authStore.canAccessAdmin) return Promise.resolve()
    if (deniedInflight) return deniedInflight
    const now = Date.now()
    if (lastDeniedRefreshAt !== 0 && now - lastDeniedRefreshAt < PERMISSION_DENIED_REFRESH_INTERVAL_MS) {
      return Promise.resolve()
    }
    lastDeniedRefreshAt = now
    const versionBefore = version.value
    const run = refresh()
      .then(async () => {
        if (version.value !== versionBefore) {
          await redirectIfCurrentRouteDenied()
        }
      })
      .catch((error) => {
        console.warn('[adminAccess] permission refresh failed:', error)
      })
      .finally(() => {
        if (deniedInflight === run) deniedInflight = null
      })
    deniedInflight = run
    return run
  }

  if (typeof window !== 'undefined') {
    detachPermissionDeniedListener?.()
    const handler = () => {
      void onPermissionDenied()
    }
    window.addEventListener(ADMIN_PERMISSION_DENIED_EVENT, handler)
    detachPermissionDeniedListener = () => window.removeEventListener(ADMIN_PERMISSION_DENIED_EVENT, handler)
  }

  // Reset on logout and on a switch to another user or role. Synchronous, so
  // a load started right after a login is never dropped by a late reset.
  // The super-admin cache follows the signed-in role from the first value on,
  // so a key left by an earlier session never outlives it.
  watch(
    () => (authUser.value ? `${authUser.value.id}:${authUser.value.role}` : null),
    (current, previous) => {
      if (previous && current !== previous) reset()
      if (role.value === 'admin') writeSuperCache(true)
      else if (role.value === 'operator') writeSuperCache(false)
      else writeSuperCache(null)
    },
    { flush: 'sync', immediate: true }
  )

  return {
    access,
    loaded,
    loading,
    version,
    isSuperAdmin,
    isOperator,
    settings,
    adminHomePath,
    landingPath,
    can,
    ensureLoaded,
    refresh,
    onPermissionDenied,
    reset
  }
})
