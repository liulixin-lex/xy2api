import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { getActivePinia } from 'pinia'

import AccountsView from '../AccountsView.vue'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminModuleId, AdminLevel } from '@/types/adminAccess'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'
import { closeSchedDrawer, markSchedChanged, useSchedLayers } from '@/composables/useSchedLayers'

const { listAccounts, getById, backendSummary, sched } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  backendSummary: vi.fn(),
  sched: { reset: vi.fn(), setOverride: vi.fn(), accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn() }
}))

vi.mock('@/api/admin/schedQuality', () => ({ default: sched, backendSummary }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getById,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      getUpstreamBalances: vi.fn().mockResolvedValue({ items: [] }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([{ id: 53, name: '晨星组', platform: 'openai', sort_order: 1 }]) }
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
          h('span', { 'data-test': 'groups' }, slots['cell-groups']?.({ row })),
          h('span', { 'data-test': 'actions' }, slots['cell-actions']?.({ row }))
        ]))
    ])
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
      ...['reset', 'exempt'].map((action) => h('button', {
        'data-test': `bulk-sched-${action}`,
        disabled: props.schedBusy,
        onClick: (event: MouseEvent) => emit('sched-action', action, event.currentTarget)
      }))
    ])
  }
})

// Turns on the "back of queue" quality filter the way the toolbar does; renders the more-actions slot.
const ToolbarStub = defineComponent({
  props: ['filters'],
  emits: ['change'],
  setup(props, { emit, slots }) {
    return () => h('div', [
      h('button', { 'data-test': 'filter-substandard', onClick: () => emit('change', { ...props.filters, sched_quality: 'rear' }) }),
      slots['more-actions']?.()
    ])
  }
})

const account = (id: number, name: string) => ({ id, name, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53] })
const summaryOf = (id: number, patch: Partial<BackendAccountSummary> = {}): BackendAccountSummary => ({
  account_id: id,
  group_status: { 53: 'pass' },
  reset_at: null,
  stop: null,
  ttfo: { first_event_p50_ms: 1200, first_output_p50_ms: 2800, gap_p50_ms: 1600, n: 30 },
  error: { n: 100, short: false, red: false, rate: 0 },
  cache: { ratio: 0.9, turns: 40, all_ratio: 0.7, all_requests: 120 },
  rear: [],
  trial: [],
  exemptions: [],
  ...patch
})
const summaries = () => [
  summaryOf(1, { group_status: { 53: 'rear' }, rear: [{ group_id: 53, model: 'gpt-6.1-sol', lines: ['ttfo'], until: '2026-11-14T12:00:00Z', exempt: false, effective: true }], ttfo: { first_event_p50_ms: 1200, first_output_p50_ms: 9100, gap_p50_ms: 7900, n: 30 } }),
  summaryOf(2),
  summaryOf(3, { group_status: { 53: 'unknown' } }),
  summaryOf(4, { stop: { source: 'manual', reason: 'manual_off', until: null } })
]

function mountView(stubs: Record<string, unknown> = {}) {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountActionMenu: true,
        AccountsToolbar: ToolbarStub,
        AccountBulkActionsBar: BulkBarStub,
        Pagination: true,
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
        Teleport: true,
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

const rowCell = (id: number, cell: string) => document.querySelector<HTMLElement>(`[data-test="row"][data-id="${id}"] [data-test="${cell}"]`)!

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
  closeSchedDrawer()
  document.documentElement.dataset.motion = 'reduced'
  listAccounts.mockReset().mockResolvedValue({ items: [account(1, 'acc-sol'), account(2, 'acc-astra'), account(3, 'acc-slow'), account(4, 'acc-empty')], total: 4, page: 1, page_size: 20, pages: 1 })
  backendSummary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: summaries() })
  sched.accountDetail.mockReset().mockReturnValue(new Promise(() => {}))
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('AccountsView quality columns', () => {
  it('reads the backend\'s own summary for the page and opens the quality panel on a pill\'s group and its substandard model', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(backendSummary).toHaveBeenCalledWith([1, 2, 3, 4], expect.objectContaining({ signal: expect.any(AbortSignal) }))
    // Dots by the spec's order: back, ok, unknown (grey), stopped (the shared hollow ring).
    const tone = (id: number) => rowCell(id, 'groups').querySelector('.status-dot')!.className
    expect([tone(1), tone(2), tone(3), tone(4)]).toEqual([
      'status-dot status-dot--back', 'status-dot status-dot--ok', 'status-dot status-dot--observe', 'status-dot status-dot--off'
    ])
    // The recent values are the whole account's: a red mark (one group and model) does not colour them (7k-2).
    expect(rowCell(1, 'recent').querySelector('[data-dim="ttfo"]')!.className).toBe('recent__v')

    rowCell(1, 'groups').querySelector<HTMLElement>('[data-test="group-pill-dot"]')!.click()
    await flushPromises()
    const { drawer } = useSchedLayers()
    expect([drawer.accountId, drawer.groupId, drawer.model]).toEqual([1, 53, 'gpt-6.1-sol'])
    wrapper.unmount()
  })

  it('asks for the summary for managers even with the quality columns hidden; a viewer asks once a quality column is shown', async () => {
    localStorage.setItem('account-hidden-columns-v2', JSON.stringify(['groups', 'sq_recent']))
    const manager = mountView()
    await flushPromises()
    expect(backendSummary).toHaveBeenCalled()
    manager.unmount()

    backendSummary.mockClear()
    seedOperator({ accounts: 'view' })
    const viewer = mountView()
    await flushPromises()
    expect(backendSummary).not.toHaveBeenCalled()
    ;(viewer.vm as unknown as { toggleColumn: (key: string) => void }).toggleColumn('sq_recent')
    await flushPromises()
    expect(backendSummary).toHaveBeenCalledTimes(1)
    viewer.unmount()
  })

  // Scenario 36: a read-only viewer gets no write entry.
  it('gives a view-only operator no write entries', async () => {
    seedOperator({ accounts: 'view' })
    const viewer = mountView()
    await flushPromises()
    expect(viewer.find('[data-test="row-edit"]').exists()).toBe(false)
    expect(viewer.find('[data-test="row-more"]').exists()).toBe(true)
    await viewer.get('[data-test="account-tools"]').trigger('click')
    expect(viewer.find('[data-test="tools-bulk-edit-filtered"]').exists()).toBe(false)
    // The pills' text zone (edit groups) is inert for a viewer; the dot still opens the quality panel.
    expect(rowCell(1, 'groups').querySelector<HTMLButtonElement>('[data-test="group-pill-text"]')!.disabled).toBe(true)
    viewer.unmount()

    seedOperator({ accounts: 'manage', usage: 'view' })
    const manager = mountView()
    await flushPromises()
    expect(manager.find('[data-test="row-edit"]').exists()).toBe(true)
    await manager.get('[data-test="account-tools"]').trigger('click')
    expect(manager.find('[data-test="tools-bulk-edit-filtered"]').exists()).toBe(true)
    expect(rowCell(1, 'groups').querySelector<HTMLButtonElement>('[data-test="group-pill-text"]')!.disabled).toBe(false)
    manager.unmount()
  })

  // The live table dims rows that left the filter; a quality change (an exemption in the panel, a recovery, a batch)
  // asks for the summary at once, never the list. Closing the panel asks for nothing.
  it('asks for the summary again after a quality change, without loading the list', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="filter-substandard"]').trigger('click')
    await flushPromises()
    rowCell(1, 'groups').querySelector<HTMLElement>('[data-test="group-pill-dot"]')!.click()
    await flushPromises()
    listAccounts.mockClear()
    backendSummary.mockClear()
    closeSchedDrawer()
    await flushPromises()
    expect(backendSummary).not.toHaveBeenCalled()
    await markSchedChanged()
    await flushPromises()
    expect(listAccounts).not.toHaveBeenCalled()
    expect(backendSummary).toHaveBeenCalledTimes(1)
    expect(backendSummary.mock.calls[0][0]).toEqual([1, 2, 3, 4])
    wrapper.unmount()
  })

  // Backend 0.6.5 keeps one batch quality action besides 恢复: 豁免 (the batch test and 后排 are gone).
  it('opens no new quality batch until the closed one has its late answers in', async () => {
    const many = Array.from({ length: 6 }, (_, i) => account(i + 1, `acc-${i + 1}`))
    listAccounts.mockResolvedValue({ items: many, total: 6, page: 1, page_size: 20, pages: 1 })
    const pending: Array<() => void> = []
    sched.setOverride.mockReset().mockImplementation(() => new Promise<void>((resolve) => pending.push(resolve)))
    const wrapper = mountView()
    await flushPromises()
    const bar = (selector: string) => wrapper.findComponent(BulkBarStub).get(selector)
    await selectFirstRow()
    await bar('[data-test="select-page"]').trigger('click')
    await bar('[data-test="bulk-sched-exempt"]').trigger('click')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    expect(sched.setOverride).toHaveBeenCalledTimes(4)

    // Close the results with four answers outstanding, then try a new batch at once.
    document.querySelector('[data-sq-batch-results]')!.closest('.modal-content')!.querySelector<HTMLElement>('.modal-footer .btn-secondary')!.click()
    await flushPromises()
    expect(bar('[data-test="bulk-sched-exempt"]').attributes('disabled')).toBeDefined()
    ;(bar('[data-test="bulk-sched-exempt"]').element as HTMLButtonElement).disabled = false
    await bar('[data-test="bulk-sched-exempt"]').trigger('click')
    await flushPromises()
    expect(document.querySelector('[data-sq-popover] .btn-primary')).toBeNull()

    pending.forEach((answer) => answer())
    await flushPromises()
    expect(sched.setOverride).toHaveBeenCalledTimes(4)
    expect(bar('[data-test="bulk-sched-exempt"]').attributes('disabled')).toBeUndefined()
    sched.setOverride.mockImplementation(() => Promise.resolve())
    await bar('[data-test="bulk-sched-exempt"]').trigger('click')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    expect(sched.setOverride.mock.calls.slice(4).map(([id]) => id)).toEqual([1, 2, 3, 4, 5, 6])
    expect(sched.setOverride.mock.calls.every(([, request]) => request.mode === 'exempt')).toBe(true)
    wrapper.unmount()
  })

  it('keeps the quality filter in the secondary account queries: select all results and edit by filter (K24)', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="filter-substandard"]').trigger('click')
    await flushPromises()
    listAccounts.mockClear()
    const bar = (selector: string) => wrapper.findComponent(BulkBarStub).get(selector)
    await selectFirstRow()
    await bar('[data-test="select-all-results"]').trigger('click')
    await flushPromises()
    // 批量更新 (edit by filter) is in the toolbar's 更多操作.
    await (wrapper.vm as unknown as { openBulkEditFiltered: () => Promise<void> }).openBulkEditFiltered()
    await flushPromises()
    const queries = listAccounts.mock.calls.filter(([, , filters]) => !(filters as { ids?: string }).ids)
    expect(queries.length).toBeGreaterThanOrEqual(2)
    for (const [, , filters] of queries) expect(filters).toMatchObject({ sched_quality: 'rear' })
    wrapper.unmount()
  })
})
