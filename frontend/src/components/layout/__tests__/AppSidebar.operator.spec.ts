import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { reactive, ref } from 'vue'
import type { MeAccess } from '@/types/adminAccess'
import { ADMIN_MODULE_IDS } from '@/types/adminAccess'

const { getMeAccessMock, commandEntries, routeState } = vi.hoisted(() => ({
  getMeAccessMock: vi.fn(),
  commandEntries: { getter: null as null | (() => Array<{ path: string }>) },
  routeState: { path: '/admin/channels/pricing' }
}))

const stores = vi.hoisted(() => ({
  auth: null as null | Record<string, unknown>,
  app: null as null | Record<string, unknown>,
  adminSettings: null as null | Record<string, unknown>
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: vi.fn() })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => stores.auth,
  useAppStore: () => stores.app,
  useAdminSettingsStore: () => stores.adminSettings,
  useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() })
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => stores.app
}))

vi.mock('@/api/admin/access', () => ({
  getMeAccess: getMeAccessMock
}))

vi.mock('@/composables/useCommandPalette', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useCommandPalette')>()
  return {
    ...actual,
    registerCommandEntries: (getter: () => Array<{ path: string }>) => {
      commandEntries.getter = getter
    }
  }
})

vi.mock('@/composables/useSidebarActiveIndicator', () => ({
  useSidebarActiveIndicator: vi.fn()
}))

vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({ canUseBatchImage: ref(false), refreshBatchImageAccess: vi.fn() })
}))

vi.mock('@/components/common/VersionBadge.vue', () => ({
  default: { name: 'VersionBadge', render: () => null }
}))

import AppSidebar from '../AppSidebar.vue'

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>'
}

function setup(
  role: 'admin' | 'operator',
  options: { simple?: boolean; modules?: MeAccess['modules']; pendingAccess?: boolean } = {}
) {
  const simple = options.simple ?? false
  getActivePinia()!.state.value.auth = { user: { id: 5, role }, runMode: simple ? 'simple' : 'standard' }
  stores.auth = reactive({
    isAdmin: role === 'admin',
    canAccessAdmin: true,
    isSimpleMode: simple,
    isAuthenticated: true
  })
  stores.app = reactive({
    sidebarCollapsed: false,
    mobileOpen: false,
    backendModeEnabled: false,
    siteName: 'Test Site',
    siteLogo: '',
    siteVersion: '',
    publicSettingsLoaded: true,
    sidebarScrollTop: 0,
    cachedPublicSettings: {
      plugin_management_enabled: true,
      risk_control_enabled: true,
      affiliate_enabled: true,
      payment_enabled: true,
      subscription_enabled: true,
      channel_monitor_enabled: true,
      custom_menu_items: []
    },
    toggleSidebar: vi.fn(),
    setMobileOpen: vi.fn()
  })
  stores.adminSettings = reactive({
    opsMonitoringEnabled: true,
    paymentEnabled: true,
    customMenuItems: [
      { id: 'tools', label: 'Admin Tools', visibility: 'admin', sort_order: 1, icon_svg: '', url: 'https://example.com' }
    ],
    fetch: vi.fn()
  })
  if (options.pendingAccess) {
    getMeAccessMock.mockReturnValue(new Promise(() => {}))
  } else {
    getMeAccessMock.mockResolvedValue({
      role,
      is_super_admin: role === 'admin',
      modules: options.modules ?? {},
      flags: [],
      custom_menu_items: []
    })
  }
  return mount(AppSidebar, {
    global: {
      plugins: [getActivePinia()!],
      stubs: { RouterLink: RouterLinkStub, Icon: true }
    }
  })
}

function adminSection(wrapper: ReturnType<typeof setup>) {
  return wrapper.findAll('.sidebar-section')[0]
}

function adminHrefs(wrapper: ReturnType<typeof setup>): string[] {
  return adminSection(wrapper)
    .findAll('a')
    .map((link) => link.attributes('href') ?? '')
}

function adminGroupLabels(wrapper: ReturnType<typeof setup>): string[] {
  return adminSection(wrapper)
    .findAll('button')
    .map((button) => button.text())
}

const allManage = Object.fromEntries(ADMIN_MODULE_IDS.map((id) => [id, 'manage'])) as MeAccess['modules']

describe('AppSidebar for operators', () => {
  beforeEach(() => {
    getMeAccessMock.mockReset()
    commandEntries.getter = null
    routeState.path = '/admin/channels/pricing'
  })

  it('shows operators only permitted entries and drops empty groups', async () => {
    const wrapper = setup('operator', { modules: { dashboard: 'view', channels: 'view', usage: 'manage' } })
    await flushPromises()

    expect(adminHrefs(wrapper)).toEqual([
      '/admin/dashboard',
      '/admin/channels/pricing',
      '/admin/channels/monitor',
      '/admin/usage'
    ])
    // The channels group keeps its children; groups whose children are all
    // denied (security audit, affiliates, orders) are dropped.
    expect(adminGroupLabels(wrapper)).toEqual(['nav.channelManagement'])
    // The logo links to the operator home.
    expect(wrapper.find('.sidebar-logo').attributes('href')).toBe('/admin/dashboard')
  })

  it('shows nothing in the admin section until me/access has loaded', async () => {
    const wrapper = setup('operator', { pendingAccess: true })
    await flushPromises()

    expect(adminHrefs(wrapper)).toEqual([])
    expect(adminGroupLabels(wrapper)).toEqual([])
  })

  it('never shows settings, plugins or custom admin items to operators', async () => {
    const operator = setup('operator', { modules: allManage })
    await flushPromises()
    const operatorHrefs = adminHrefs(operator)
    expect(operatorHrefs).toContain('/admin/users')
    expect(operatorHrefs).not.toContain('/admin/settings')
    expect(operatorHrefs).not.toContain('/admin/plugins')
    expect(operatorHrefs).not.toContain('/custom/tools')
    const paletteOperator = commandEntries.getter!().map((entry) => entry.path)
    expect(paletteOperator).not.toContain('/admin/settings')
    expect(paletteOperator).not.toContain('/admin/plugins')
    expect(paletteOperator).not.toContain('/custom/tools')
    operator.unmount()

    const simpleOperator = setup('operator', { simple: true, modules: allManage })
    await flushPromises()
    const simpleHrefs = adminHrefs(simpleOperator)
    expect(simpleHrefs).toContain('/keys')
    expect(simpleHrefs).not.toContain('/admin/settings')
    expect(simpleHrefs).not.toContain('/custom/tools')
    simpleOperator.unmount()

    // A super admin still sees all three.
    const superAdmin = setup('admin')
    await flushPromises()
    const superHrefs = adminHrefs(superAdmin)
    expect(superHrefs).toContain('/admin/settings')
    expect(superHrefs).toContain('/admin/plugins')
    expect(superHrefs).toContain('/custom/tools')
  })
})
