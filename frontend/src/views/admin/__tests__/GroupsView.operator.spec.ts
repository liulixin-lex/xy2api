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
  updateGroup: vi.fn(),
  createGroup: vi.fn(),
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
      create: api.createGroup,
      update: api.updateGroup,
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

function seedOperator(flags: AdminFlagId[], accountLevel?: 'view' | 'manage') {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { groups: 'manage', ...(accountLevel ? { accounts: accountLevel } : {}) },
    flags,
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

function mountView() {
  return mount(GroupsView, {
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
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        GroupCategoryField: true,
        ReasoningEffortPolicyFields: { template: '<div />', methods: { validate: () => true } },
        CodexManifestAccountsField: true,
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
 }

async function deleteNonEmptyGroup() {
  const wrapper = mountView()
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
    api.updateGroup.mockResolvedValue({})
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


describe('GroupsView protected edit prices', () => {
  it.each([false, true, 'admin'] as const)('renames a group with NULL prices and omits protected prices when flag=%s', async (flag) => {
    seedOperator(flag ? ['users.balance_adjust'] : [])
    if (flag === 'admin') getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' } }
    api.listGroups.mockResolvedValue({ items: [createGroup({ platform: 'openai' })], total: 1 })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-tour="edit-group-form-name"]').setValue('Renamed')
    for (const fieldset of wrapper.findAll('[data-testid="group-protected-pricing"]')) {
      expect(fieldset.attributes('disabled') !== undefined).toBe(!flag)
    }
    expect(wrapper.get('[data-testid="edit-force-openai-fast"]').attributes('disabled') !== undefined).toBe(!flag)
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    const payload = api.updateGroup.mock.calls.at(-1)![1]
    expect(payload.name).toBe('Renamed')
    const protectedFields = ['platform', 'force_openai_fast', 'rate_multiplier', 'subscription_type', 'daily_limit_usd', 'weekly_limit_usd', 'monthly_limit_usd', 'long_context_pricing_enabled', 'long_context_pricing_scope', 'long_context_pricing_models', 'model_pricing', 'image_rate_independent', 'image_rate_multiplier', 'batch_image_discount_multiplier', 'batch_image_hold_multiplier', 'video_rate_independent', 'video_rate_multiplier', 'peak_rate_enabled', 'peak_start', 'peak_end', 'peak_rate_multiplier', 'profit_control_enabled', 'profit_min_margin', 'profit_safety_buffer', 'image_price_1k', 'image_price_2k', 'image_price_4k', 'video_price_480p', 'video_price_720p', 'video_price_1080p', 'video_model_prices', 'web_search_price_per_call', 'search_price_per_1k', 'audio_realtime_price_per_min', 'audio_tts_price_per_million_chars', 'audio_stt_price_per_hour', 'free_openai_fast', 'max_reasoning_effort', 'reasoning_effort_mappings']
    for (const field of protectedFields) expect(Object.hasOwn(payload, field), field).toBe(!!flag)
    wrapper.unmount()
  })
})


describe('GroupsView account copying', () => {
  it.each([
    { accountLevel: 'manage' as const, balance: false, allowed: true },
    { accountLevel: 'view' as const, balance: false, allowed: false },
    { accountLevel: undefined, balance: false, allowed: false },
    { accountLevel: undefined, balance: true, allowed: true }
  ])('requires account management or the balance flag: $accountLevel / $balance', async ({ accountLevel, balance, allowed }) => {
    vi.clearAllMocks()
    seedOperator(balance ? ['users.balance_adjust'] : [], accountLevel)
    api.listGroups.mockResolvedValue({ items: [createGroup(), createGroup({ id: 2, name: 'source' })], total: 2 })
    api.createGroup.mockResolvedValue({ id: 3 })
    api.updateGroup.mockResolvedValue({})
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'admin.groups.createGroup')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="group-create-copy-accounts"]').exists()).toBe(allowed)
    if (allowed) await wrapper.get('[data-testid="group-create-copy-accounts"] select').setValue('2')
    await wrapper.get('#create-group-form input[type="text"]').setValue('created')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(api.createGroup).toHaveBeenCalledTimes(1)
    const created = api.createGroup.mock.calls[0][0]
    if (allowed) expect(created.copy_accounts_from_group_ids).toEqual([2])
    else expect(created).not.toHaveProperty('copy_accounts_from_group_ids')
    if (!balance) expect(created.is_exclusive).toBe(true)

    await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="group-edit-copy-accounts"]').exists()).toBe(allowed)
    if (allowed) await wrapper.get('[data-testid="group-edit-copy-accounts"] select').setValue('2')
    if (!balance) {
      for (const fieldset of wrapper.findAll('[data-testid="group-protected-pricing"]')) expect(fieldset.attributes('disabled')).toBeDefined()
    }
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(api.updateGroup).toHaveBeenCalledTimes(1)
    const updated = api.updateGroup.mock.calls[0][1]
    if (allowed) expect(updated.copy_accounts_from_group_ids).toEqual([2])
    else expect(updated).not.toHaveProperty('copy_accounts_from_group_ids')
    if (!balance) {
      expect(updated).not.toHaveProperty('rate_multiplier')
      expect(updated).not.toHaveProperty('platform')
    }
    wrapper.unmount()
  })
})
