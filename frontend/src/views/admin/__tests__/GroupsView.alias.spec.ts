import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const { listGroups, createGroup, updateGroup, categoriesApi, showSuccess, showError, showWarning, authState } = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  categoriesApi: { list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() },
  showSuccess: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  authState: { isSimpleMode: false }
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      create: createGroup,
      update: updateGroup,
      duplicate: vi.fn(),
      getAll: vi.fn(async () => []),
      getModelAllowlistCandidates: vi.fn(async () => []),
      getUsageSummary: vi.fn(async () => []),
      getCapacitySummary: vi.fn(async () => []),
      getLiveCapability: vi.fn(async () => ({ supported: false })),
      delete: vi.fn(),
      updateSortOrder: vi.fn()
    },
    accounts: { list: vi.fn(async () => ({ items: [], total: 0 })), getById: vi.fn() }
  }
}))

vi.mock('@/api/admin/groupCategories', () => ({ default: categoriesApi }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError, showWarning })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: vi.fn(() => false), nextStep: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const sourceGroup = {
  id: 42,
  name: 'Primary',
  description: null,
  platform: 'openai',
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
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-10-22T00:00:00Z',
  updated_at: '2026-10-22T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  sort_order: 10
} as unknown as AdminGroup

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] }, columns: { type: Array, default: () => [] } },
  template: `
    <div>
      <div data-test="columns">{{ columns.map((col) => col.key).join(',') }}</div>
      <div v-for="row in data" :key="row.id">
        <span data-test="name-cell"><slot name="cell-name" :row="row" :value="row.name" /></span>
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
})

const SelectStub = defineComponent({
  props: ['modelValue', 'options'],
  emits: ['update:modelValue', 'change'],
  template: `
    <select :value="modelValue" @change="$emit('update:modelValue', $event.target.value); $emit('change')">
      <option v-for="option in options" :key="String(option.value)" :value="option.value">{{ option.label }}</option>
    </select>
  `
})

// Attached, so focus can be checked (7j-3).
function mountView() {
  return mount(GroupsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        TablePageLayout: { template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>' },
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true
      }
    }
  })
}

enableAutoUnmount(afterEach)

const openCreate = async (wrapper: ReturnType<typeof mountView>) => {
  await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-tour="group-form-name"]').setValue('New group')
}
const aliasInput = (wrapper: ReturnType<typeof mountView>) => wrapper.get('[data-test="group-alias-input"]')

describe('GroupsView group alias', () => {
  beforeEach(() => {
    authState.isSimpleMode = false
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [listGroups, createGroup, categoriesApi.list, categoriesApi.setGroupCategories, showSuccess, showError]) fn.mockReset()
    listGroups.mockResolvedValue({ items: [{ ...sourceGroup, alias: '主力' }, { ...sourceGroup, id: 43, name: 'Gemini Ultra' }], total: 2, page: 1, page_size: 20, pages: 1 })
    categoriesApi.list.mockResolvedValue([])
    createGroup.mockResolvedValue({ ...sourceGroup, id: 77, name: 'New group' })
  })
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows the alias pill beside the group name, or the automatic text without one', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('[data-test="name-cell"] .alias-pill').map((pill) => pill.text())).toEqual(['主力', 'Gem'])
  })

  it('keeps a too long alias from being sent, and sends one that fits', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openCreate(wrapper)
    await aliasInput(wrapper).setValue('ABCD')
    ;(document.activeElement as HTMLElement | null)?.blur()
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(createGroup).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="group-alias-error"]').text()).toBe('admin.groups.alias.tooLong')
    // High in a long form whose save button is at the bottom: the input takes focus, which brings it into view (7j-3).
    expect(document.activeElement).toBe(aliasInput(wrapper).element)

    await aliasInput(wrapper).setValue('新')
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(createGroup).toHaveBeenCalledWith(expect.objectContaining({ alias: '新' }))
  })

  it('shows the backend\'s INVALID_GROUP_ALIAS under the alias input, not as a toast, and focuses the input', async () => {
    createGroup.mockRejectedValue({ status: 400, reason: 'INVALID_GROUP_ALIAS', message: 'alias display width must be at most 2' })
    const wrapper = mountView()
    await flushPromises()
    await openCreate(wrapper)
    await aliasInput(wrapper).setValue('稳')
    ;(document.activeElement as HTMLElement | null)?.blur()
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-test="group-alias-error"]').text()).toBe('admin.groups.alias.invalid')
    expect(showError).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(aliasInput(wrapper).element)
  })
})
