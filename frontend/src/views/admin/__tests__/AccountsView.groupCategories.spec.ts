import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import AccountsView from '../AccountsView.vue'
import { backendSummary } from '@/api/admin/schedQuality'

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

const openGroupPicker = async (wrapper: View) => {
  await wrapper.get('[data-account-name="acc-7"] [data-test="group-pill-text"]').trigger('click')
  await flushPromises()
}
// The picker is teleported to the body.
const groupCheckbox = (id: number) =>
  document.querySelector<HTMLInputElement>(`[data-group-picker] input[type="checkbox"][value="${id}"]`)!
const tick = async (id: number, checked: boolean) => {
  const box = groupCheckbox(id)
  box.checked = checked
  box.dispatchEvent(new Event('change'))
  await flushPromises()
}
/** A click outside the picker closes it (and saves a change). */
const clickOutside = async () => {
  document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
  await flushPromises()
}
const pillTexts = (wrapper: View) => wrapper.findAll('[data-account-name="acc-7"] [data-test="group-pill-text"]').map((pill) => pill.text())

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

  it('opens the group picker beside the groups cell and saves only group_ids once it closes', async () => {
    updateAccount.mockResolvedValue({ ...freshAccount, group_ids: [3, 1, 2] })
    const wrapper = mountView()
    await flushPromises()
    // Pills in the groups page order, each with its automatic text (no alias set)
    expect(pillTexts(wrapper)).toEqual(['Alp', 'Gam'])

    await openGroupPicker(wrapper)
    expect(getById).toHaveBeenCalledWith(7)
    expect(document.querySelector('[data-group-picker]')!.getAttribute('aria-label')).toBe('admin.groupCategories.editAccountGroups')
    expect(document.querySelector('[data-test="group-picker-tab-3"]')).not.toBeNull()
    expect(groupCheckbox(3).checked).toBe(true)

    await tick(2, true)
    expect(updateAccount).not.toHaveBeenCalled()
    vi.mocked(backendSummary).mockClear()
    await clickOutside()

    expect(updateAccount).toHaveBeenCalledTimes(1)
    // The pills' dots follow the new groups: the summary is asked again at once.
    expect(backendSummary).toHaveBeenCalledTimes(1)
    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [3, 1, 2] })
    expect(document.querySelector('[data-group-picker]')).toBeNull()
    // The saved groups show at once, in the groups page order
    expect(pillTexts(wrapper)).toEqual(['Alp', 'Bet', 'Gam'])
    wrapper.unmount()
  })

  it('empties the cell when an edit leaves the account in no group: the answer has no group_ids (7d-1)', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(pillTexts(wrapper)).toEqual(['Alp', 'Gam'])
    // The backend omits an empty group_ids (and an empty limit, here max_sessions).
    const { group_ids: _omitted, ...answer } = { ...listRow, max_sessions: undefined }
    ;(wrapper.vm as unknown as { handleAccountUpdated: (a: unknown) => void }).handleAccountUpdated({ ...answer, name: 'acc-7' })
    await flushPromises()

    expect(pillTexts(wrapper)).toEqual([])
    expect(wrapper.find('[data-account-name="acc-7"] [data-test="group-pill-empty"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('keeps the groups structure of the full answer out of the row when the picker removes the last groups (7d-1)', async () => {
    const { group_ids: _omitted, ...withoutGroupIds } = freshAccount
    updateAccount.mockResolvedValue({ ...withoutGroupIds, account_groups: [{ group_id: 3 }] })
    const wrapper = mountView()
    await flushPromises()
    await openGroupPicker(wrapper)
    await tick(1, false)
    await tick(3, false)
    await clickOutside()

    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [] })
    expect(pillTexts(wrapper)).toEqual([])
    expect(wrapper.find('[data-account-name="acc-7"] [data-test="group-pill-empty"]').exists()).toBe(true)
    const row = (wrapper.vm as unknown as { rows: Array<Record<string, unknown>> }).rows[0]
    expect(row.group_ids).toEqual([])
    expect('groups' in row || 'account_groups' in row).toBe(false)
    wrapper.unmount()
  })

  it('shows the new groups in the cell while saving and puts the old ones back when the save fails', async () => {
    let fail!: (error: unknown) => void
    updateAccount.mockReturnValue(new Promise((_, reject) => { fail = reject }))
    const wrapper = mountView()
    await flushPromises()
    await openGroupPicker(wrapper)
    await tick(2, true)
    await clickOutside()
    // Optimistic: only this cell, before the answer.
    expect(pillTexts(wrapper)).toEqual(['Alp', 'Bet', 'Gam'])
    expect(document.querySelector('[data-test="group-picker-foot"]')!.textContent).toContain('admin.accounts.groupPicker.saving')

    fail({ status: 500, message: 'boom' })
    await flushPromises()
    expect(pillTexts(wrapper)).toEqual(['Alp', 'Gam'])
    expect(showError).toHaveBeenCalledTimes(1)
    expect(document.querySelector('[data-group-picker]')).toBeNull()
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

    await openGroupPicker(wrapper)
    await tick(1, false)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [3] })
    expect(wrapper.get('[data-account-name="acc-7"]').classes()).toContain('opacity-60')
    wrapper.unmount()
  })
})
