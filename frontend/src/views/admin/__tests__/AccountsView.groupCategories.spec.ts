import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getById,
  updateAccount,
  getAllGroups,
  categoriesApi,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getById: vi.fn(),
  updateAccount: vi.fn(),
  getAllGroups: vi.fn(),
  categoriesApi: { list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() },
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getById,
      update: updateAccount,
      getBatchTodayStats: vi.fn(async () => ({ stats: {} })),
      getBatchRecentHealth: vi.fn(async () => ({ stats: {} })),
      getUpstreamBillingProbeSettings: vi.fn(async () => ({ enabled: true })),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      bulkUpdate: vi.fn()
    },
    proxies: { getAll: vi.fn(async () => []) },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/api/admin/groupCategories', () => ({ default: categoriesApi }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess, showWarning: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const groups = [
  { id: 1, name: 'Alpha', platform: 'anthropic', status: 'active' },
  { id: 2, name: 'Beta', platform: 'anthropic', status: 'active' },
  { id: 3, name: 'Gamma', platform: 'anthropic', status: 'active' }
]

const listRow = {
  id: 7,
  name: 'acc-7',
  platform: 'anthropic',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 1,
  priority: 1,
  group_ids: [3, 1],
  extra: {},
  credentials: {}
}

const freshAccount = { ...listRow, groups: [groups[2], groups[0]] }

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-account-name="row.name">
        <slot name="cell-groups" :row="row" />
      </div>
    </div>
  `
})

const AccountTableFiltersStub = defineComponent({
  props: ['filters'],
  emits: ['update:filters', 'change'],
  template: `<button data-test="pick-category" @click="$emit('update:filters', { ...filters, group_category: '3' }); $emit('change')">pick</button>`
})

const AccountBulkActionsBarStub = defineComponent({
  emits: ['select-all-results'],
  template: '<button data-test="select-all-results" @click="$emit(\'select-all-results\')">select all</button>'
})

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: { template: '<div><slot name="after" /></div>' },
        AccountTableFilters: AccountTableFiltersStub,
        AccountBulkActionsBar: AccountBulkActionsBarStub,
        BaseDialog: { props: ['show', 'title'], emits: ['close'], template: '<div v-if="show" data-test="base-dialog"><h3>{{ title }}</h3><slot /><slot name="footer" /></div>' },
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountUsageCell: true,
        UpstreamBillingRateCell: true,
        GroupBadge: { props: ['name'], template: '<span class="group-badge">{{ name }}</span>' },
        HelpTooltip: true,
        Icon: true
      }
    }
  })
}

type View = ReturnType<typeof mountView>

const openGroupsDialog = async (wrapper: View) => {
  await wrapper.get('[data-account-name="acc-7"] [data-testid="account-groups-cell"]').trigger('click')
  await flushPromises()
}
const groupCheckbox = (wrapper: View, id: number) =>
  wrapper.get<HTMLInputElement>(`[data-test="account-groups-dialog"] input[type="checkbox"][value="${id}"]`)

const pickCategory = async (wrapper: View) => {
  await wrapper.get('[data-test="pick-category"]').trigger('click')
  await new Promise((resolve) => setTimeout(resolve, 350))
  await flushPromises()
}

describe('admin AccountsView group categories', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'etag', data: null })
    getById.mockReset().mockResolvedValue(freshAccount)
    updateAccount.mockReset()
    getAllGroups.mockReset().mockResolvedValue(groups)
    categoriesApi.list.mockReset().mockResolvedValue([{ id: 3, name: 'Public', sort_order: 0, group_ids: [1] }])
    showError.mockReset()
    showSuccess.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('opens the group dialog from the groups cell and saves only group_ids', async () => {
    updateAccount.mockResolvedValue({ ...freshAccount, group_ids: [3, 1, 2] })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-account-name="acc-7"]').text()).toContain('Gamma')

    await openGroupsDialog(wrapper)
    expect(getById).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-test="base-dialog"] h3').text()).toBe('admin.groupCategories.editAccountGroups')
    expect(wrapper.find('[data-test="group-category-tabs"]').exists()).toBe(true)
    expect(groupCheckbox(wrapper, 3).element.checked).toBe(true)

    await groupCheckbox(wrapper, 2).setValue(true)
    await wrapper.get('[data-test="account-groups-confirm"]').trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledTimes(1)
    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [3, 1, 2] })
    expect(wrapper.find('[data-test="account-groups-dialog"]').exists()).toBe(false)
    expect(wrapper.get('[data-account-name="acc-7"]').text()).toContain('Beta')
    wrapper.unmount()
  })

  it('carries group_category into the list, select-all, bulk edit, export and upstream billing filters', async () => {
    const wrapper = mountView()
    await flushPromises()
    await pickCategory(wrapper)

    expect(listAccounts).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ group_category: '3' }), expect.anything())
    await wrapper.get('[data-test="select-all-results"]').trigger('click')
    await flushPromises()
    expect(listAccounts).toHaveBeenLastCalledWith(1, 1000, expect.objectContaining({ group_category: '3', lite: '1' }))

    const vm = wrapper.vm as unknown as Record<string, () => Record<string, unknown>>
    expect(vm.buildBulkEditFilterSnapshot()).toMatchObject({ group_category: '3' })
    expect(vm.buildAccountQueryFilters()).toMatchObject({ group_category: '3' })
    expect(vm.buildUpstreamBillingRateFilters()).toMatchObject({ group_category: '3' })
    wrapper.unmount()
  })

  it('removes a saved row that no longer matches the category filter', async () => {
    updateAccount.mockResolvedValue({ ...freshAccount, group_ids: [3] })
    const wrapper = mountView()
    await flushPromises()
    await pickCategory(wrapper)

    await openGroupsDialog(wrapper)
    await groupCheckbox(wrapper, 1).setValue(false)
    await wrapper.get('[data-test="account-groups-confirm"]').trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [3] })
    expect(wrapper.find('[data-account-name="acc-7"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('skips auto-refresh while the group dialog is open', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const wrapper = mountView()
    await flushPromises()

    await openGroupsDialog(wrapper)
    await vi.advanceTimersByTimeAsync(7000)
    expect(listWithEtag).not.toHaveBeenCalled()

    await wrapper.findAll('[data-test="base-dialog"] button').find((button) => button.text() === 'common.cancel')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="account-groups-dialog"]').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(7000)
    expect(listWithEtag).toHaveBeenCalled()
    expect(updateAccount).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
