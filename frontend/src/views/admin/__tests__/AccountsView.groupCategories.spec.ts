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

// AccountsView loads the quality summary on mount; a real request would fail after the test file finishes.
vi.mock('@/api/admin/schedQuality', () => ({ backendSummary: vi.fn().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] }), default: {} }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getById,
      update: updateAccount,
      getBatchTodayStats: vi.fn(async () => ({ stats: {} })),
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} }),
      getUpstreamBillingProbeSettings: vi.fn(async () => ({ enabled: true })),
      getUpstreamBalances: vi.fn().mockResolvedValue({ items: [] }),
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
  props: { data: { type: Array, default: () => [] }, rowClass: { type: Function, default: undefined } },
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-account-name="row.name" :class="rowClass?.(row)">
        <slot name="cell-select" :row="row" />
        <slot name="cell-groups" :row="row" />
      </div>
    </div>
  `
})

const AccountsToolbarStub = defineComponent({
  props: ['filters'],
  emits: ['change'],
  template: `<button data-test="pick-category" @click="$emit('change', { ...filters, group_category: '3' })">pick</button>`
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
        AccountsToolbar: AccountsToolbarStub,
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
  await wrapper.get('[data-account-name="acc-7"] [data-test="group-pill-text"]').trigger('click')
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
    // Pills in the groups page order, each with its automatic text (no alias set)
    expect(wrapper.findAll('[data-account-name="acc-7"] [data-test="group-pill-text"]').map((pill) => pill.text())).toEqual(['Alp', 'Gam'])

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
    // The saved groups show at once, in the groups page order
    expect(wrapper.findAll('[data-account-name="acc-7"] [data-test="group-pill-text"]').map((pill) => pill.text())).toEqual(['Alp', 'Bet', 'Gam'])
    wrapper.unmount()
  })

  it('carries group_category into the list, select-all, bulk edit and export filters', async () => {
    const wrapper = mountView()
    await flushPromises()
    await pickCategory(wrapper)

    expect(listAccounts).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ group_category: '3' }), expect.anything())
    // The bulk bar shows only while something is selected.
    await wrapper.findAll('[data-account-name] input[type="checkbox"]')[0].trigger('change')
    await wrapper.get('[data-test="select-all-results"]').trigger('click')
    await flushPromises()
    expect(listAccounts).toHaveBeenLastCalledWith(1, 1000, expect.objectContaining({ group_category: '3', lite: '1' }))

    // Bulk edit by filter and the export share one filter snapshot
    const vm = wrapper.vm as unknown as Record<string, () => Record<string, unknown>>
    expect(vm.buildFilterSnapshot()).toMatchObject({ group_category: '3' })
    wrapper.unmount()
  })

  it('keeps a saved row that no longer matches the category filter in place, dimmed', async () => {
    updateAccount.mockResolvedValue({ ...freshAccount, group_ids: [3] })
    const wrapper = mountView()
    await flushPromises()
    await pickCategory(wrapper)

    await openGroupsDialog(wrapper)
    await groupCheckbox(wrapper, 1).setValue(false)
    await wrapper.get('[data-test="account-groups-confirm"]').trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [3] })
    expect(wrapper.get('[data-account-name="acc-7"]').classes()).toContain('opacity-60')
    wrapper.unmount()
  })
})
