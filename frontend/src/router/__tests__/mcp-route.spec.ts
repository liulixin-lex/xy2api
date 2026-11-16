import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { auth, app } = vi.hoisted(() => ({
  auth: { checkAuth: vi.fn(), isAuthenticated: false, isAdmin: false, isSimpleMode: false, canAccessAdmin: false, user: { role: 'user' } },
  app: { siteName: 'XY2API', backendModeEnabled: false, publicSettingsLoaded: true, cachedPublicSettings: { mcp_enabled: true, mcp_user_enabled: false, mcp_user_menu_visible: false } }
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => app }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('@/stores/adminCompliance', () => ({ useAdminComplianceStore: () => ({}) }))
vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({ startNavigation: vi.fn(), endNavigation: vi.fn() })
}))
vi.mock('@/composables/useRoutePrefetch', () => ({ useRoutePrefetch: () => ({ triggerPrefetch: vi.fn() }) }))
vi.mock('@/views/user/MCPView.vue', () => ({ default: { template: '<div>MCP</div>' } }))
vi.mock('@/views/user/DashboardView.vue', () => ({ default: { template: '<div>Dashboard</div>' } }))
vi.mock('@/views/auth/LoginView.vue', () => ({ default: { template: '<div>Login</div>' } }))
vi.mock('@/router/title', () => ({ resolveRouteDocumentTitle: () => 'MCP OAuth', applyDocumentTitle: vi.fn() }))

beforeEach(() => { vi.spyOn(window, 'scrollTo').mockImplementation(() => {}) })

afterEach(() => { vi.restoreAllMocks(); auth.isAuthenticated = false; app.cachedPublicSettings.mcp_user_enabled = false })

describe('MCP OAuth route', () => {
  it('requires login and preserves the entire authorization query in the login redirect', async () => {
    const { default: router } = await import('@/router')
    const record = router.getRoutes().find(route => route.name === 'MCPOAuthAuthorize')
    expect(record?.path).toBe('/oauth/management-mcp/authorize')
    expect(record?.meta.requiresAuth).toBe(true)
    expect(record?.meta.requiresAdmin).toBe(false)
    const path = '/oauth/management-mcp/authorize?' + new URLSearchParams({
      response_type: 'code', client_id: 'desktop', redirect_uri: 'http://127.0.0.1:54321/callback',
      resource: 'https://api.example.com/api/v1/mcp', state: 'a+b&x=1',
      code_challenge: 'a'.repeat(43), code_challenge_method: 'S256'
    }).toString()
    await router.push(path)
    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe(path)
  })
  it('keeps grant management available but blocks OAuth consent when role access is disabled', async () => {
    const { default: router } = await import('@/router')
    auth.isAuthenticated = true
    app.cachedPublicSettings.mcp_user_menu_visible = true
    await router.push('/settings/mcp')
    expect(router.currentRoute.value.path).toBe('/settings/mcp')
    await router.push('/oauth/management-mcp/authorize?state=test')
    expect(router.currentRoute.value.path).toBe('/dashboard')
  })

  it('allows direct access when the role is enabled and its menu is hidden', async () => {
    const { default: router } = await import('@/router')
    auth.isAuthenticated = true
    app.cachedPublicSettings.mcp_user_enabled = true
    app.cachedPublicSettings.mcp_user_menu_visible = false
    await router.push('/settings/mcp')
    expect(router.currentRoute.value.path).toBe('/settings/mcp')
  })

})
