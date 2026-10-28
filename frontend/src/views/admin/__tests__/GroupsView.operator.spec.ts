import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

import type { AdminGroup } from '@/types'
import type { AdminFlagId, MeAccess } from '@/types/adminAccess'
import { useAdminAccessStore } from '@/stores/adminAccess'
import GroupsView from '../GroupsView.vue'

const api = vi.hoisted(() => ({
  listGroups: vi.fn(),
  deleteGroup: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: api.listGroups,
      getAll: vi.fn().mockResolvedValue([]),
      getModelAllowlistCandidates: vi.fn().mockResolvedValue([]),
      getUsageSummary: vi.fn().mockResolvedValue([]),
      getCapacitySummary: vi.fn().mockResolvedValue([]),
      getLiveCapability: vi.fn().mockResolvedValue({ supported: false }),
      create: vi.fn(),
      update: vi.fn(),
      delete: api.deleteGroup,
      updateSortOrder: vi.fn()
    },
    accounts: { list: vi.fn().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 }) }
  }
}))

vi.mock('@/api/admin/groupCategories', () => ({
  default: { list: vi.fn().mockResolvedValue([]), save: vi.fn(), setGroupCategories: vi.fn() }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: api.showError, showSuccess: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: false })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() })
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const createGroup = (overrides: Partial<AdminGroup> = {}): AdminGroup => ({
  id: 1,
  name: 'Core Anthropic',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 3,
  active_account_count: 2,
  rate_limited_account_count: 1,
  model_allowlist: undefined,
  sort_order: 10,
  ...overrides,
})

function seedOperator(flags: AdminFlagId[]) {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { groups: 'manage' },
    flags,
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

async function deleteNonEmptyGroup() {
  const wrapper = mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: {
          props: ['columns', 'data'],
          emits: ['sort'],
          template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>'
        },
        ConfirmDialog: {
          props: ['show', 'title'],
          emits: ['confirm', 'cancel'],
          template: '<button v-if="show" :data-test="title" @click="$emit(\'confirm\')">{{ title }}</button>'
        },
        Pagination: true,
        BaseDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: { template: '<div><slot /></div>' }
      }
    }
  })
  await flushPromises()
  await wrapper.findAll('button').find((button) => button.text() === 'common.delete')!.trigger('click')
  await wrapper.get('[data-test="admin.groups.deleteGroup"]').trigger('click')
  await flushPromises()
  wrapper.unmount()
  expect(api.deleteGroup).toHaveBeenCalledWith(1)
  return api.showError.mock.calls.map(([message]) => message)
}

describe('GroupsView delete of a group that still has accounts', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    api.listGroups.mockResolvedValue({ items: [createGroup()], total: 1, page: 1, page_size: 20, pages: 1 })
    api.deleteGroup.mockRejectedValue({ status: 409, code: 'CONFLICT', reason: 'GROUP_NOT_EMPTY', message: 'group still has accounts' })
  })

  it('explains the empty-group rule to an operator without "Adjust balance"', async () => {
    seedOperator([])
    expect(await deleteNonEmptyGroup()).toEqual(['admin.operatorAccess.groupDeleteNeedsEmpty'])
  })

  it('keeps the original message for an operator with "Adjust balance" and for a super admin', async () => {
    seedOperator(['users.balance_adjust'])
    expect(await deleteNonEmptyGroup()).toEqual(['admin.groups.failedToDelete'])

    api.showError.mockClear()
    useAdminAccessStore().reset()
    getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }
    expect(await deleteNonEmptyGroup()).toEqual(['admin.groups.failedToDelete'])
  })
})
