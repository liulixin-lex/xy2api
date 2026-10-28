import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { getActivePinia } from 'pinia'
import { ADMIN_PAGES_IN_SIDEBAR_ORDER } from '@/stores/adminAccess'
import { ADMIN_MODULE_IDS, type MeAccess } from '@/types/adminAccess'

type NavigationGuard = (
  to: Record<string, any>,
  from: Record<string, any>,
  next: ReturnType<typeof vi.fn>
) => Promise<void>

interface RouteRecordLike {
  path: string
  redirect?: unknown
  meta?: Record<string, unknown>
  children?: RouteRecordLike[]
}

const routerHarness = vi.hoisted(() => ({
  guard: null as NavigationGuard | null,
  routes: [] as RouteRecordLike[]
}))

const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  isAuthenticated: true,
  isAdmin: false,
  canAccessAdmin: true,
  isSimpleMode: false,
  hasPendingAuthSession: false
}))

const appStore = vi.hoisted(() => ({
  siteName: 'XY2API',
  backendModeEnabled: false,
  publicSettingsLoaded: true,
  cachedPublicSettings: {},
  fetchPublicSettings: vi.fn()
}))

const getMeAccessMock = vi.hoisted(() => vi.fn())

vi.mock('vue-router', () => ({
  createWebHistory: vi.fn(() => ({})),
  createRouter: vi.fn((options: { routes: RouteRecordLike[] }) => {
    routerHarness.routes = options.routes
    return {
      beforeEach: vi.fn((guard: NavigationGuard) => {
        routerHarness.guard = guard
      }),
      afterEach: vi.fn(),
      onError: vi.fn()
    }
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStore
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] })
}))

vi.mock('@/stores/adminCompliance', () => ({
  useAdminComplianceStore: () => ({
    initialized: true,
    fetchStatus: vi.fn(),
    requireAcknowledgement: vi.fn()
  })
}))

vi.mock('@/api/admin/access', () => ({
  getMeAccess: getMeAccessMock
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false }
  })
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn()
  })
}))

function flattenRoutes(routes: RouteRecordLike[], parent = ''): RouteRecordLike[] {
  return routes.flatMap((route) => {
    const path = route.path.startsWith('/') ? route.path : `${parent}/${route.path}`
    return [{ ...route, path }, ...flattenRoutes(route.children ?? [], path)]
  })
}

function findRoute(path: string): RouteRecordLike {
  const route = flattenRoutes(routerHarness.routes).find((item) => item.path === path)
  if (!route) throw new Error(`route ${path} not found`)
  return route
}

async function runGuard(path: string) {
  if (!routerHarness.guard) throw new Error('router guard was not registered')
  const route = findRoute(path)
  const next = vi.fn()
  await routerHarness.guard(
    { path, fullPath: path, name: 'AdminRoute', params: {}, meta: { ...(route.meta ?? {}) } },
    {},
    next
  )
  return next
}

function seedOperator(modules: MeAccess['modules']) {
  getActivePinia()!.state.value.auth = { user: { id: 9, role: 'operator' }, runMode: 'standard' }
  getMeAccessMock.mockResolvedValue({
    role: 'operator',
    is_super_admin: false,
    modules,
    flags: [],
    custom_menu_items: []
  })
}

describe('admin route permission meta', () => {
  beforeAll(async () => {
    await import('@/router')
  })

  beforeEach(() => {
    authStore.isAuthenticated = true
    authStore.isAdmin = false
    authStore.canAccessAdmin = true
    authStore.isSimpleMode = false
    getMeAccessMock.mockReset()
  })

  it('declares meta.permission on every admin page route', () => {
    const routes = flattenRoutes(routerHarness.routes)
    const adminPages = routes.filter((route) => route.meta?.requiresAdmin === true)
    const allowed = new Set<string>([...ADMIN_MODULE_IDS, 'super_only'])

    expect(adminPages.length).toBe(ADMIN_PAGES_IN_SIDEBAR_ORDER.length)
    for (const route of adminPages) {
      expect(allowed.has(String(route.meta?.permission)), route.path).toBe(true)
    }

    // Every /admin path is either a permission-checked page or a plain redirect.
    for (const route of routes.filter((item) => item.path.startsWith('/admin'))) {
      if (route.redirect !== undefined) continue
      expect(route.meta?.requiresAdmin, route.path).toBe(true)
    }

    // The sidebar order list (used for the operator home) matches the routes.
    for (const page of ADMIN_PAGES_IN_SIDEBAR_ORDER) {
      expect(findRoute(page.path).meta?.permission, page.path).toBe(page.permission)
    }
    expect(findRoute('/admin/settings').meta?.permission).toBe('super_only')
    expect(findRoute('/admin/plugins').meta?.permission).toBe('super_only')
  })

  it('sends an operator without the module to the first permitted admin page', async () => {
    seedOperator({ groups: 'view', usage: 'manage' })

    expect(await runGuard('/admin/users')).toHaveBeenCalledWith('/admin/groups')
    expect(await runGuard('/admin/settings')).toHaveBeenCalledWith('/admin/groups')
    expect(await runGuard('/admin/plugins')).toHaveBeenCalledWith('/admin/groups')

    const allowed = await runGuard('/admin/usage')
    expect(allowed).toHaveBeenCalledOnce()
    expect(allowed).toHaveBeenCalledWith()
    expect(getMeAccessMock).toHaveBeenCalledTimes(1)
  })

  it('sends an operator with no admin page to the profile', async () => {
    seedOperator({})

    expect(await runGuard('/admin/dashboard')).toHaveBeenCalledWith('/profile')
  })

  it('lets a super admin through without loading me/access', async () => {
    authStore.isAdmin = true
    getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }

    const next = await runGuard('/admin/settings')
    expect(next).toHaveBeenCalledWith()
    expect(getMeAccessMock).not.toHaveBeenCalled()
  })
})
