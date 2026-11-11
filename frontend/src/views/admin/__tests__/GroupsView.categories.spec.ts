import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '@/views/admin/GroupsView.vue'

const {
  listGroups,
  createGroup,
  updateGroup,
  duplicateGroup,
  categoriesApi,
  showSuccess,
  showError,
  showWarning,
  authState
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
  duplicateGroup: vi.fn(),
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
      duplicate: duplicateGroup,
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

const categories = [
  { id: 3, name: 'Public', sort_order: 0, group_ids: [42] },
  { id: 4, name: 'Monitor', sort_order: 1, group_ids: [] }
]

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] }, columns: { type: Array, default: () => [] } },
  template: `
    <div>
      <div data-test="columns">{{ columns.map((col) => col.key).join(',') }}</div>
      <div v-for="row in data" :key="row.id">
        <span v-if="columns.some((col) => col.key === 'category')" data-test="category-cell"><slot name="cell-category" :row="row" /></span>
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

function mountView() {
  return mount(GroupsView, {
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

type View = ReturnType<typeof mountView>

const openCreate = async (wrapper: View) => {
  await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-tour="group-form-name"]').setValue('New group')
}
const openEdit = async (wrapper: View) => {
  await wrapper.findAll('button').find((button) => button.text() === 'common.edit')!.trigger('click')
  await flushPromises()
}
const tick = (wrapper: View, id: number) => wrapper.get(`[data-test="group-category-field"] input[data-category-id="${id}"]`).setValue(true)

describe('GroupsView group categories', () => {
  beforeEach(() => {
    authState.isSimpleMode = false
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [listGroups, createGroup, updateGroup, duplicateGroup, categoriesApi.list, categoriesApi.setGroupCategories, showSuccess, showError, showWarning]) {
      fn.mockReset()
    }
    listGroups.mockResolvedValue({ items: [sourceGroup], total: 1, page: 1, page_size: 20, pages: 1 })
    categoriesApi.list.mockResolvedValue(categories)
    createGroup.mockResolvedValue({ ...sourceGroup, id: 77, name: 'New group' })
    updateGroup.mockResolvedValue(sourceGroup)
    duplicateGroup.mockResolvedValue({ ...sourceGroup, id: 43, name: 'Primary (Copy)' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('shows the category column, manager and filter, and sends the filter with the list request', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="columns"]').text().split(',')).toContain('category')
    expect(wrapper.get('[data-test="category-cell"]').text()).toBe('Public')
    expect(wrapper.find('[data-test="group-category-manage"]').exists()).toBe(true)

    await wrapper.get('select[data-test="group-category-filter"]').setValue('3')
    await flushPromises()
    expect(listGroups).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ group_category: '3' }), expect.anything())
  })

  it('saves categories after the group is created', async () => {
    categoriesApi.setGroupCategories.mockResolvedValue({ group_id: 77, category_ids: [4] })
    const wrapper = mountView()
    await flushPromises()
    await openCreate(wrapper)
    await tick(wrapper, 4)
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledTimes(1)
    expect(createGroup.mock.calls[0][0]).not.toHaveProperty('category_ids')
    expect(categoriesApi.setGroupCategories).toHaveBeenCalledWith(77, [4])
    expect(createGroup.mock.invocationCallOrder[0]).toBeLessThan(categoriesApi.setGroupCategories.mock.invocationCallOrder[0])
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.groupCreated')
  })

  it('warns, without reporting a failed create, when only the categories fail', async () => {
    categoriesApi.setGroupCategories.mockRejectedValue({ message: 'category save failed' })
    const wrapper = mountView()
    await flushPromises()
    await openCreate(wrapper)
    await tick(wrapper, 3)
    await wrapper.get('#create-group-form').trigger('submit')
    await flushPromises()

    expect(createGroup).toHaveBeenCalledTimes(1)
    expect(showWarning).toHaveBeenCalledWith('admin.groupCategories.groupSavedCategoriesFailed')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.find('#create-group-form').exists()).toBe(false)
  })

  it('sends no category request for an unchanged edit, and the new set after a change', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openEdit(wrapper)
    expect((wrapper.get('[data-test="group-category-field"] input[data-category-id="3"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(categoriesApi.setGroupCategories).not.toHaveBeenCalled()

    categoriesApi.setGroupCategories.mockResolvedValue({ group_id: 42, category_ids: [3, 4] })
    await openEdit(wrapper)
    await tick(wrapper, 4)
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()
    expect(categoriesApi.setGroupCategories).toHaveBeenCalledWith(42, [3, 4])
  })

  it('copies the categories to a duplicated group', async () => {
    categoriesApi.setGroupCategories.mockResolvedValue({ group_id: 43, category_ids: [3] })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(duplicateGroup).toHaveBeenCalledWith(42)
    expect(categoriesApi.setGroupCategories).toHaveBeenCalledWith(43, [3])
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
  })

  it('renders no category UI and loads no categories in simple mode', async () => {
    authState.isSimpleMode = true
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="columns"]').text().split(',')).not.toContain('category')
    expect(wrapper.find('[data-test="group-category-filter"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="group-category-manage"]').exists()).toBe(false)
    await wrapper.get('[data-tour="groups-create-btn"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="group-category-field"]').exists()).toBe(false)
    expect(categoriesApi.list).not.toHaveBeenCalled()
  })
})
