import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import AccountsView from '../AccountsView.vue'
import AccountActionMenu from '@/components/admin/account/AccountActionMenu.vue'

const {
  listAccounts,
  listWithEtag,
  getById,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings,
  getAllProxies,
  getAllGroups,
  refreshCredentials,
  setSchedulable,
  bulkUpdate,
  showError,
  showWarning,
  showInfo
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getById: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  refreshCredentials: vi.fn(),
  setSchedulable: vi.fn(),
  bulkUpdate: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  showInfo: vi.fn()
}))

// AccountsView loads the quality summary on mount; a real request would fail after the test file finishes.
vi.mock('@/api/admin/schedQuality', () => ({ backendSummary: vi.fn().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] }), default: {} }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getById,
      listWithEtag,
      getBatchTodayStats,
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} }),
      getUpstreamBillingProbeSettings,
      getUpstreamBalances: vi.fn().mockResolvedValue({ items: [] }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      setSchedulable,
      bulkUpdate,
      refreshCredentials
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showWarning, showSuccess: vi.fn(), showInfo })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DataTableStub = defineComponent({
  props: { data: { type: Array, default: () => [] } },
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-account-name="row.name">
        <slot name="cell-groups" :row="row" />
        <slot name="cell-schedulable" :row="row" />
        <span data-test="account-rate"><slot name="cell-rate_multiplier" :row="row" /></span>
        <slot name="cell-capacity" :row="row" />
        <span data-test="select-account"><slot name="cell-select" :row="row" /></span>
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
})

const AccountGroupsCellStub = defineComponent({
  props: { groups: { type: Array, default: () => [] } },
  template: '<span data-test="account-groups">{{ groups.map(group => group.name).join(",") }}</span>'
})

const AccountCapacityCellStub = defineComponent({
  props: { account: { type: Object, required: true } },
  template: '<span data-test="account-group-concurrency">{{ account.current_group_concurrency }}</span>'
})

const EditAccountModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="edit-account">{{ show ? account?.name : "" }}</div>'
})

const AccountTestModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="test-account">{{ show ? account?.name : "" }}</div>'
})

const AccountStatsModalStub = defineComponent({
  props: { show: Boolean, account: { type: Object, default: null } },
  template: '<div data-test="stats-account">{{ show ? account?.name : "" }}</div>'
})

function mountView(stubActionMenu = true) {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountsToolbar: { emits: ['refresh'], template: '<div><button data-test="refresh-accounts" @click="$emit(\'refresh\')">Refresh</button><slot name="more-actions" /></div>' },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: stubActionMenu,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: AccountTestModalStub,
        AccountStatsModal: AccountStatsModalStub,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: EditAccountModalStub,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: AccountCapacityCellStub,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: AccountGroupsCellStub,
        AccountGroupsCellButton: AccountGroupsCellStub,
        AccountUsageCell: true,
        UpstreamBillingRateCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: stubActionMenu
      }
    }
  })
}

const listRow = {
  id: 42,
  name: 'compact row',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  concurrency: 2,
  priority: 1,
  group_ids: [7],
  extra: {},
  credentials: {}
}

const fullAccount = {
  ...listRow,
  groups: [{ id: 7, name: 'codex', platform: 'openai' }],
  account_groups: [{ account_id: 42, group_id: 7 }],
  credentials: { api_key: 'redacted' },
  extra: { detail_only: true }
}

describe('admin AccountsView lite account list', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'compact-etag', data: null })
    getById.mockReset().mockResolvedValue(fullAccount)
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockReset().mockResolvedValue({ enabled: true })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([{ id: 7, name: 'codex', platform: 'openai' }])
    refreshCredentials.mockReset()
    setSchedulable.mockReset().mockImplementation(async (id, schedulable) => ({ id, schedulable }))
    bulkUpdate.mockReset()
    showError.mockReset()
    showWarning.mockReset()
    showInfo.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('toggles scheduling immediately without a dialog', async () => {
    const wrapper = mountView()
    await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    expect(toggle.attributes('type')).toBe('button')
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.attributes('aria-label')).toContain('compact row')
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenLastCalledWith(42, false)
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.text()).not.toMatch(/controlTitle|diagnosticsTitle|failureDomains|globalAccountEnabled/)
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenLastCalledWith(42, true)
    expect(toggle.attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })

  it('lets the latest click win: a second click while the first is pending sends the new value, and the older answer is not written', async () => {
    const pending: Array<(value: unknown) => void> = []
    setSchedulable.mockImplementation(() => new Promise(resolve => pending.push(resolve)))
    const wrapper = mountView(); await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click')
    await toggle.trigger('click')
    expect(setSchedulable.mock.calls).toEqual([[42, false], [42, true]])
    expect(toggle.attributes('aria-checked')).toBe('true')
    pending[0]({ id: 42, schedulable: false }); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    pending[1]({ id: 42, schedulable: true }); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })

  it('keeps the confirmed state and allows retry after a failed scheduling update', async () => {
    setSchedulable.mockRejectedValueOnce(new Error('temporary failure'))
    const wrapper = mountView(); await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.attributes('disabled')).toBeUndefined()
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToToggleSchedulable')
    await toggle.trigger('click'); await flushPromises()
    expect(setSchedulable).toHaveBeenCalledTimes(2)
    expect(toggle.attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('flips the switch before the request finishes and keeps it through a refresh that lands meanwhile', async () => {
    let resolveToggle!: (value: unknown) => void
    setSchedulable.mockImplementationOnce(() => new Promise(resolve => { resolveToggle = resolve }))
    const wrapper = mountView(); await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(toggle.attributes('aria-busy')).toBe('true')
    listAccounts.mockResolvedValueOnce({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    await wrapper.get('[data-test="refresh-accounts"]').trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    resolveToggle({ id: 42, schedulable: false }); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(toggle.attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('rolls the optimistic switch back when the update fails', async () => {
    let rejectToggle!: (reason: unknown) => void
    setSchedulable.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectToggle = reject }))
    const wrapper = mountView(); await flushPromises()
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click')
    expect(toggle.attributes('aria-checked')).toBe('false')
    rejectToggle(new Error('temporary failure')); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedToToggleSchedulable')
    wrapper.unmount()
  })

  it('ignores a row refresh that was asked for before a completed account toggle', async () => {
    vi.useFakeTimers()
    const wrapper = mountView(); await flushPromises()
    let resolveRows!: (value: unknown) => void
    listAccounts.mockReturnValueOnce(new Promise(resolve => { resolveRows = resolve }))
    await vi.advanceTimersByTimeAsync(3000)
    expect(listAccounts).toHaveBeenLastCalledWith(1, 1, expect.objectContaining({ ids: '42' }), expect.anything())
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    resolveRows({ items: [listRow], total: 1, page: 1, page_size: 1, pages: 1 })
    await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('preserves the confirmed switch when a manual refresh returns an older account snapshot', async () => {
    const wrapper = mountView(); await flushPromises()
    let resolveList!: (value: unknown) => void
    listAccounts.mockReturnValueOnce(new Promise(resolve => { resolveList = resolve }))
    await wrapper.get('[data-test="refresh-accounts"]').trigger('click')
    const toggle = wrapper.get('button[role="switch"]')
    await toggle.trigger('click'); await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    resolveList({ items: [listRow], total: 1, page: 1, page_size: 20, pages: 1 })
    await flushPromises()
    expect(toggle.attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })

  it('keeps unrelated account changes from a list refresh during a toggle', async () => {
    const second = { ...listRow, id: 43, name: 'second' }
    listAccounts.mockResolvedValue({ items: [listRow, second], total: 2, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountView(); await flushPromises()
    let resolveList!: (value: unknown) => void
    listAccounts.mockReturnValueOnce(new Promise(resolve => { resolveList = resolve }))
    await wrapper.get('[data-test="refresh-accounts"]').trigger('click')
    await wrapper.get('[data-account-name="compact row"] button[role="switch"]').trigger('click'); await flushPromises()
    resolveList({ items: [listRow, { ...second, schedulable: false, rate_multiplier: 0.3 }], total: 2, page: 1, page_size: 20, pages: 1 })
    await flushPromises()
    expect(wrapper.get('[data-account-name="compact row"] button[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-account-name="second"] button[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-account-name="second"] [data-test="account-rate"]').text()).toBe('0.30x')
    wrapper.unmount()
  })

  it('updates a rate-only change from the 3-second row refresh', async () => {
    vi.useFakeTimers()
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-test="account-rate"]').text()).toBe('1.00x')
    listAccounts.mockResolvedValueOnce({ items: [{ ...listRow, rate_multiplier: 0.035 }], total: 1, page: 1, page_size: 1, pages: 1 })
    await vi.advanceTimersByTimeAsync(3000); await flushPromises()
    expect(wrapper.get('[data-test="account-rate"]').text()).toBe('0.035x')
    expect(wrapper.get('button[role="switch"]').attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })

  it('redraws a row when only its group concurrency changes on the 3-second row refresh', async () => {
    vi.useFakeTimers()
    const groupRow = { ...listRow, current_concurrency: 2 }
    listAccounts.mockResolvedValueOnce({ items: [{ ...groupRow, current_group_concurrency: 1 }], total: 1, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountView(); await flushPromises()
    expect(wrapper.get('[data-test="account-group-concurrency"]').text()).toBe('1')
    listAccounts.mockResolvedValueOnce({ items: [{ ...groupRow, current_group_concurrency: 2 }], total: 1, page: 1, page_size: 1, pages: 1 })
    await vi.advanceTimersByTimeAsync(3000); await flushPromises()
    expect(wrapper.get('[data-test="account-group-concurrency"]').text()).toBe('2')
    wrapper.unmount()
  })

  it('flips the selected switches at once on a bulk change and rolls back the ones that failed', async () => {
    listAccounts.mockResolvedValue({ items: [listRow, { ...listRow, id: 43, name: 'second' }], total: 2, page: 1, page_size: 20, pages: 1 })
    let finishBulk!: (value: unknown) => void
    bulkUpdate.mockReturnValueOnce(new Promise(resolve => { finishBulk = resolve }))
    const wrapper = mountView(); await flushPromises()
    for (const checkbox of wrapper.findAll('[data-test="select-account"] input')) await checkbox.setValue(true)
    const bar = wrapper.findComponent({ name: 'AccountBulkActionsBar' })
    const toggles = wrapper.findAll('button[role="switch"]')
    bar.vm.$emit('toggle-schedulable', false); await flushPromises()
    expect(bulkUpdate).toHaveBeenCalledWith([42, 43], { schedulable: false })
    expect(toggles.map(toggle => toggle.attributes('aria-checked'))).toEqual(['false', 'false'])
    finishBulk({ success: 1, failed: 1, success_ids: [42], failed_ids: [43] }); await flushPromises()
    expect(toggles.map(toggle => toggle.attributes('aria-checked'))).toEqual(['false', 'true'])
    expect(showError).toHaveBeenCalledWith('admin.accounts.bulkSchedulablePartial')
    wrapper.unmount()
  })

  it('keeps lite=1 on the initial list request', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledWith(
      1,
      20,
      expect.objectContaining({ lite: '1' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    wrapper.unmount()
  })

  it('maps group_ids through the group catalog for the groups pills', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('[data-test="group-pill-text"]').map(pill => pill.text())).toEqual(['cod'])
    wrapper.unmount()
  })

  it('keeps the action menu open during internal scrolling but closes it on table scrolling', async () => {
    const wrapper = mountView(false)
    await flushPromises()

    await wrapper.get('[data-test="row-more"]').trigger('click')
    const menu = new DOMWrapper(document.body.querySelector('.action-menu-content')!)
    menu.element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(true)

    menu.get('button').element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(true)

    wrapper.getComponent(DataTableStub).element.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(wrapper.findComponent(AccountActionMenu).props('show')).toBe(false)
    wrapper.unmount()
  })

  it('keeps lite=1 on the 3-second row refresh, which asks by id', async () => {
    vi.useFakeTimers()
    const wrapper = mountView()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(listAccounts).toHaveBeenLastCalledWith(
      1,
      1,
      expect.objectContaining({ lite: '1', ids: '42' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    wrapper.unmount()
  })

  it('loads the full account by id before opening edit, test, and stats actions', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="row-edit"]').trigger('click')
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-test="edit-account"]').text()).toBe('compact row')

    const menu = wrapper.findComponent(AccountActionMenu)
    menu.vm.$emit('test', listRow)
    await flushPromises()
    expect(getById).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="test-account"]').text()).toBe('compact row')

    menu.vm.$emit('stats', listRow)
    await flushPromises()
    expect(getById).toHaveBeenCalledTimes(3)
    expect(wrapper.get('[data-test="stats-account"]').text()).toBe('compact row')
    wrapper.unmount()
  })

  it('shows the warning and patches the account after a partial Antigravity refresh', async () => {
    refreshCredentials.mockResolvedValue({
      account: { ...fullAccount, name: 'refreshed account' },
      message: 'Token refreshed, but project_id is temporarily unavailable',
      warning: 'missing_project_id_temporary'
    })
    const wrapper = mountView(false)
    await flushPromises()

    wrapper.findComponent(AccountActionMenu).vm.$emit('refresh-token', listRow)
    await flushPromises()

    expect(refreshCredentials).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-account-name]').attributes('data-account-name')).toBe('refreshed account')
    expect(showWarning).toHaveBeenCalledWith('Token refreshed, but project_id is temporarily unavailable')
    wrapper.unmount()
  })

  it('keeps the group concurrency when a single-account response patches the row', async () => {
    listAccounts.mockResolvedValue({ items: [{ ...listRow, current_group_concurrency: 3 }], total: 1, page: 1, page_size: 20, pages: 1 })
    refreshCredentials.mockResolvedValue({ account: { ...fullAccount, name: 'refreshed account' } })
    const wrapper = mountView(false)
    await flushPromises()

    wrapper.findComponent(AccountActionMenu).vm.$emit('refresh-token', listRow)
    await flushPromises()

    expect(wrapper.get('[data-account-name]').attributes('data-account-name')).toBe('refreshed account')
    expect(wrapper.get('[data-test="account-group-concurrency"]').text()).toBe('3')
    wrapper.unmount()
  })

  it('shows an error and keeps the modal closed when detail loading fails', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    getById.mockRejectedValueOnce(new Error('detail failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="row-edit"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('detail failed')
    // 编辑弹窗按需挂载：从未打开时不渲染，打开过则内容为空
    const editModal = wrapper.find('[data-test="edit-account"]')
    expect(editModal.exists() ? editModal.text() : '').toBe('')
    consoleError.mockRestore()
    wrapper.unmount()
  })
})
