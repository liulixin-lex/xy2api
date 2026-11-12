import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { getActivePinia } from 'pinia'

import AccountsView from '../AccountsView.vue'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminModuleId, AdminLevel } from '@/types/adminAccess'
import type { AccountRecentStats, AccountRecentStatsBatch, SchedAccountSummary } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import { closeSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'

const { listAccounts, getById, getRecentStats, sched } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  getRecentStats: vi.fn(),
  sched: { summary: vi.fn(), reset: vi.fn(), startTest: vi.fn(), setOverride: vi.fn(), accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn() }
}))

vi.mock('@/api/admin/schedQuality', () => ({ default: sched }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getById,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getRecentStats,
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      getUpstreamBalances: vi.fn().mockResolvedValue({ balances: [] }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([{ id: 53, name: '晨星组', platform: 'openai' }]) }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token', isSimpleMode: false }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true })
  }
})

// Renders rows in the order given, with the recent status, groups and actions cells.
const DataTableStub = defineComponent({
  props: ['columns', 'data'],
  setup(props, { slots }) {
    return () => h('div', [
      h('div', { 'data-test': 'columns' }, (props.columns as Array<{ key: string }>).map((column) => column.key).join(',')),
      ...(props.data as Array<{ id: number }>).map((row) =>
        h('div', { 'data-test': 'row', 'data-id': row.id }, [
          slots['cell-select']?.({ row }),
          h('span', { 'data-test': 'recent' }, slots['cell-sq_recent']?.({ row })),
          h('span', { 'data-test': 'groups' }, slots['cell-sq_groups']?.({ row })),
          h('span', { 'data-test': 'membership' }, slots['cell-groups']?.({ row })),
          h('span', { 'data-test': 'actions' }, slots['cell-actions']?.({ row }))
        ]))
    ])
  }
})

const RouterLinkStub = defineComponent({
  name: 'RouterLink',
  props: ['to'],
  setup(props, { slots }) {
    return () => h('a', { href: `${props.to.path}?account_id=${props.to.query.account_id}` }, slots.default?.())
  }
})

const PaginationStub = defineComponent({
  emits: ['update:page'],
  setup(_, { emit }) {
    return () => h('button', { 'data-test': 'next-page', onClick: () => emit('update:page', 2) })
  }
})

// Selects the page and starts quality batches; its buttons are disabled while a batch is busy.
const BulkBarStub = defineComponent({
  props: ['schedBusy'],
  emits: ['select-page', 'select-all-results', 'sched-action'],
  setup(props, { emit }) {
    return () => h('div', [
      h('button', { 'data-test': 'select-page', onClick: () => emit('select-page') }),
      h('button', { 'data-test': 'select-all-results', onClick: () => emit('select-all-results') }),
      ...['reset', 'demote', 'test'].map((action) => h('button', {
        'data-test': `bulk-sched-${action}`,
        disabled: props.schedBusy,
        onClick: (event: MouseEvent) => emit('sched-action', action, event.currentTarget)
      }))
    ])
  }
})

// Turns on the "cooling" filter the way the real filter bar does.
const FiltersStub = defineComponent({
  props: ['filters'],
  emits: ['update:filters', 'change'],
  setup(props, { emit }) {
    return () => h('button', { 'data-test': 'filter-cooling', onClick: () => { emit('update:filters', { ...props.filters, sched_quality: 'cooling' }); emit('change') } })
  }
})

const account = (id: number, name: string) => ({ id, name, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53] })
const base = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
const summaries = (): SchedAccountSummary[] => [
  { ...structuredClone(base), account_id: 1, model: 'gpt-6.1-sol' },
  { ...structuredClone(base), account_id: 2, model: 'gpt-6-astra', ttfo: { p90_ms: 9100, timeout: false, n: 140 }, cooling: [] },
  { ...structuredClone(base), account_id: 3, model: 'gpt-6.1-sol', ttfo: { p90_ms: null, timeout: true, n: 90 }, cooling: [] },
  { ...structuredClone(base), account_id: 4, model: null, ttfo: { p90_ms: null, timeout: false, n: 0 }, cooling: [] }
]

function mountView(stubs: Record<string, unknown> = {}) {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        RouterLink: RouterLinkStub,
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountActionMenu: true,
        AccountStatusIndicator: true,
        AccountTableActions: true,
        AccountTableFilters: FiltersStub,
        AccountBulkActionsBar: BulkBarStub,
        Pagination: PaginationStub,
        ConfirmDialog: true,
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
        Icon: true,
        ...stubs
      }
    }
  })
}

// The bulk bar shows only while something is selected.
const selectFirstRow = async () => {
  document.querySelector<HTMLInputElement>('[data-test="row"] input[type="checkbox"]')!.click()
  await flushPromises()
}

function seedOperator(modules: Partial<Record<AdminModuleId, AdminLevel>>) {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = { role: 'operator', is_super_admin: false, modules, flags: [], settings: {} as never, custom_menu_items: [] }
  access.loaded = true
}

const stats = (id: number, ttftMs: number): AccountRecentStats => ({
  account_id: id,
  ttft: { p50_ms: ttftMs, n: 30, tier: 'good' },
  error: { rate: 0, bad: 0, n: 100, tier: 'good' },
  cache: { ratio: 0.9, n: 40, tier: 'good' }
})
const batch = (items: AccountRecentStats[]): AccountRecentStatsBatch => ({ as_of: '2026-11-12T12:00:00Z', items })

const rowCell = (id: number, cell: string) => document.querySelector<HTMLElement>(`[data-test="row"][data-id="${id}"] [data-test="${cell}"]`)!

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
  closeSchedDrawer()
  document.documentElement.dataset.motion = 'reduced'
  listAccounts.mockReset().mockResolvedValue({ items: [account(1, 'acc-sol'), account(2, 'acc-astra'), account(3, 'acc-slow'), account(4, 'acc-empty')], total: 4, page: 1, page_size: 20, pages: 1 })
  sched.summary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: summaries() })
  sched.accountDetail.mockReset().mockReturnValue(new Promise(() => {}))
  getRecentStats.mockReset().mockImplementation(async (ids: number[]) => batch(ids.map((id) => stats(id, 2800))))
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('AccountsView quality columns', () => {
  it('loads the summary without a model and opens the drawer on a capsule\'s own group', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(sched.summary).toHaveBeenCalledWith([1, 2, 3, 4], expect.anything())
    const { drawer } = useSchedLayers()
    rowCell(1, 'groups').querySelector<HTMLElement>('.sq-capsule')!.click()
    expect([drawer.accountId, drawer.model, drawer.groupId]).toEqual([1, 'gpt-6.1-sol', 53])
    wrapper.unmount()
  })

  it('loads the summary for managers even with the groups column hidden', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['sq_groups']))
    const wrapper = mountView()
    await flushPromises()
    expect(sched.summary).toHaveBeenCalledTimes(1)
    wrapper.unmount()

    sched.summary.mockClear()
    seedOperator({ accounts: 'view' })
    const viewer = mountView()
    await flushPromises()
    expect(sched.summary).not.toHaveBeenCalled()
    viewer.unmount()
  })

  // Scenario 26: a slow response for an earlier page never replaces the current page's recent status.
  it('keeps the current page\'s recent status when an earlier, slower response arrives late', async () => {
    let answerFirst: (value: AccountRecentStatsBatch) => void = () => {}
    getRecentStats
      .mockImplementationOnce(() => new Promise((resolve) => { answerFirst = resolve }))
      .mockImplementationOnce(async (ids: number[]) => batch(ids.map((id) => stats(id, 4100))))
    const wrapper = mountView()
    await flushPromises()
    expect(rowCell(1, 'recent').querySelectorAll('[data-test="recent-skeleton"]')).toHaveLength(3)

    listAccounts.mockResolvedValueOnce({ items: [account(5, 'acc-five')], total: 5, page: 2, page_size: 4, pages: 2 })
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    expect(getRecentStats.mock.calls.map(([ids]) => ids)).toEqual([[1, 2, 3, 4], [5]])
    expect(rowCell(5, 'recent').querySelector('[data-test="recent-ttft"]')!.textContent).toBe('4.1s')

    answerFirst(batch([1, 2, 3, 4, 5].map((id) => stats(id, 12600))))
    await flushPromises()
    expect(rowCell(5, 'recent').querySelector('[data-test="recent-ttft"]')!.textContent).toBe('4.1s')
    wrapper.unmount()
  })

  it('requests no recent status while the column is hidden and loads it once the column is shown', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['sq_recent']))
    const wrapper = mountView()
    await flushPromises()
    expect(getRecentStats).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="columns"]').text().split(',')).not.toContain('sq_recent')

    ;(wrapper.vm as unknown as { toggleColumn: (key: string) => void }).toggleColumn('sq_recent')
    await flushPromises()
    expect(getRecentStats).toHaveBeenCalledWith([1, 2, 3, 4], expect.anything())
    expect(rowCell(1, 'recent').querySelector('[data-test="recent-ttft"]')!.textContent).toBe('2.8s')
    wrapper.unmount()
  })

  // Scenario 36: a read-only viewer gets no write entry; without usage records the recent status is plain text.
  it('gives a view-only operator no write entries and no usage link without the usage permission', async () => {
    const tools = { AccountTableActions: { template: '<div><slot name="after" /></div>' }, Teleport: true }
    seedOperator({ accounts: 'view' })
    const viewer = mountView(tools)
    await flushPromises()
    expect(viewer.find('[data-test="row-edit"]').exists()).toBe(false)
    expect(viewer.find('[data-test="row-more"]').exists()).toBe(true)
    await viewer.get('button[title="admin.accounts.moreActions"]').trigger('click')
    expect(viewer.find('[data-test="tools-bulk-edit-filtered"]').exists()).toBe(false)
    expect(rowCell(1, 'recent').querySelector('a')).toBeNull()
    expect(rowCell(1, 'recent').querySelector('[data-test="recent-stats"]')!.tagName).toBe('DIV')
    // Membership is plain badges for a viewer: no button, no edit name, no Tab stop (red team FA-2).
    const membership = rowCell(1, 'membership').querySelector<HTMLElement>('[data-testid="account-groups-cell"]')!
    expect(membership.textContent).toContain('晨星组')
    expect([membership.tagName, membership.getAttribute('aria-label'), membership.tabIndex]).toEqual(['SPAN', null, -1])
    expect(rowCell(1, 'membership').querySelector('button')).toBeNull()
    viewer.unmount()

    seedOperator({ accounts: 'manage', usage: 'view' })
    const manager = mountView(tools)
    await flushPromises()
    expect(manager.find('[data-test="row-edit"]').exists()).toBe(true)
    await manager.get('button[title="admin.accounts.moreActions"]').trigger('click')
    expect(manager.find('[data-test="tools-bulk-edit-filtered"]').exists()).toBe(true)
    expect(rowCell(1, 'recent').querySelector('a')!.getAttribute('href')).toBe('/admin/usage?account_id=1')
    expect(rowCell(1, 'membership').querySelector('button[data-testid="account-groups-cell"]')!.getAttribute('aria-label')).toBe('admin.groupCategories.editAccountGroupsAria:{"name":"acc-sol"}')
    manager.unmount()
  })

  // Contract delta D2: under a quality filter, closing the drawer reloads the list; otherwise only the summary.
  it('reloads the list when the drawer closes while a quality filter is on', async () => {
    const wrapper = mountView()
    await flushPromises()
    rowCell(1, 'groups').querySelector<HTMLElement>('.sq-capsule')!.click()
    await flushPromises()
    listAccounts.mockClear()
    sched.summary.mockClear()
    closeSchedDrawer()
    await flushPromises()
    expect(listAccounts).not.toHaveBeenCalled()
    expect(sched.summary).toHaveBeenCalledTimes(1)

    await wrapper.findComponent(FiltersStub).trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 400))
    await flushPromises()
    rowCell(1, 'groups').querySelector<HTMLElement>('.sq-capsule')!.click()
    await flushPromises()
    listAccounts.mockClear()
    closeSchedDrawer()
    await flushPromises()
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(listAccounts.mock.calls[0][2]).toMatchObject({ sched_quality: 'cooling' })
    wrapper.unmount()
  })

  it('opens no new quality batch until the closed one has its late answers in', async () => {
    const many = Array.from({ length: 6 }, (_, i) => account(i + 1, `acc-${i + 1}`))
    listAccounts.mockResolvedValue({ items: many, total: 6, page: 1, page_size: 20, pages: 1 })
    const pending: Array<() => void> = []
    sched.setOverride.mockReset().mockImplementation(() => new Promise<void>((resolve) => pending.push(resolve)))
    sched.startTest.mockReset().mockResolvedValue({ tests: [{ model: 'm', test_id: 't', started: true, why: '' }] })
    const wrapper = mountView()
    await flushPromises()
    const bar = (selector: string) => wrapper.findComponent(BulkBarStub).get(selector)
    await selectFirstRow()
    await bar('[data-test="select-page"]').trigger('click')
    await bar('[data-test="bulk-sched-demote"]').trigger('click')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    expect(sched.setOverride).toHaveBeenCalledTimes(4)

    // Close the results with four answers outstanding, then try a new batch at once.
    document.querySelector('[data-sq-batch-results]')!.closest('.modal-content')!.querySelector<HTMLElement>('.modal-footer .btn-secondary')!.click()
    await flushPromises()
    expect(bar('[data-test="bulk-sched-test"]').attributes('disabled')).toBeDefined()
    ;(bar('[data-test="bulk-sched-test"]').element as HTMLButtonElement).disabled = false
    await bar('[data-test="bulk-sched-test"]').trigger('click')
    await flushPromises()
    const testConfirm = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-batch-confirm]'))
      .find((el) => el.textContent?.trim() === 'admin.schedQuality.action.test')
    expect(testConfirm()).toBeUndefined()

    pending.forEach((answer) => answer())
    await flushPromises()
    expect(sched.setOverride).toHaveBeenCalledTimes(4)
    expect(bar('[data-test="bulk-sched-test"]').attributes('disabled')).toBeUndefined()
    await bar('[data-test="bulk-sched-test"]').trigger('click')
    await flushPromises()
    testConfirm()!.click()
    await flushPromises()
    expect(sched.startTest.mock.calls.map(([id]) => id)).toEqual([1, 2, 3, 4, 5, 6])
    expect(sched.setOverride).toHaveBeenCalledTimes(4)
    wrapper.unmount()
  })

  it('keeps the cooling filter in the secondary account queries: select all results and edit by filter (K24)', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findComponent(FiltersStub).trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 400))
    await flushPromises()
    listAccounts.mockClear()
    const bar = (selector: string) => wrapper.findComponent(BulkBarStub).get(selector)
    await selectFirstRow()
    await bar('[data-test="select-all-results"]').trigger('click')
    await flushPromises()
    // 批量更新 (edit by filter) is in the toolbar's 更多操作.
    await (wrapper.vm as unknown as { openBulkEditFiltered: () => Promise<void> }).openBulkEditFiltered()
    await flushPromises()
    expect(listAccounts.mock.calls.length).toBeGreaterThanOrEqual(2)
    for (const [, , filters] of listAccounts.mock.calls) expect(filters).toMatchObject({ sched_quality: 'cooling' })
    wrapper.unmount()
  })
})
