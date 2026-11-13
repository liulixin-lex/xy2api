import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'
import type { SchedAccountSummary } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import { closeSchedDrawer, openSchedDrawer } from '@/composables/useSchedLayers'
import { recoveryLines } from '@/composables/useAccountRecovery'

// 恢复 on the account list (FINAL §5, contract §6.5): the row button, its result line, the batch, the refreshes.

const { listAccounts, getById, sched, toast } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  sched: { summary: vi.fn(), reset: vi.fn(), accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn() },
  toast: { showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() }
}))
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ manage: true })
})

vi.mock('@/api/admin/schedQuality', () => ({ default: sched }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getById,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
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
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token', isSimpleMode: false, isAdmin: false }) }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return {
    useAdminPermission: () => ({
      canView: computed(() => true),
      canManage: computed(() => perms.manage),
      has: () => true,
      isSuperAdmin: computed(() => true)
    })
  }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true })
  }
})

const DataTableStub = defineComponent({
  props: ['columns', 'data'],
  setup(props, { slots }) {
    return () => h('div', (props.data as Array<{ id: number }>).map((row) =>
      h('div', { 'data-test': 'row', 'data-id': row.id }, [
        slots['cell-select']?.({ row }),
        h('span', { 'data-test': 'actions' }, slots['cell-actions']?.({ row })),
        h('div', { 'data-test': 'detail' }, slots['row-detail']?.({ row }))
      ])))
  }
})

const BulkBarStub = defineComponent({
  props: ['selectedIds', 'schedBusy', 'recoverBusy'],
  emits: ['select-page', 'sched-action'],
  setup(props, { emit }) {
    return () => h('div', { 'data-test': 'bulk-bar' }, [
      h('button', { 'data-test': 'select-page', onClick: () => emit('select-page') }),
      h('button', {
        'data-test': 'bulk-sched-reset',
        disabled: props.schedBusy || props.recoverBusy,
        onClick: (event: MouseEvent) => emit('sched-action', 'reset', event.currentTarget)
      })
    ])
  }
})

// Turns on the 不达标 quality filter the way the filter bar does.
const FiltersStub = defineComponent({
  props: ['filters'],
  emits: ['update:filters', 'change'],
  setup(props, { emit }) {
    return () => h('button', { 'data-test': 'filter-substandard', onClick: () => { emit('update:filters', { ...props.filters, sched_quality: 'substandard' }); emit('change') } })
  }
})

const account = (id: number, patch: Record<string, unknown> = {}) =>
  ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53], ...patch })
const base = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
const summaryOf = (id: number): SchedAccountSummary => ({ ...structuredClone(base), account_id: id })

const ROWS = [account(1, { status: 'error' }), account(2), account(3), account(4, { schedulable: false })]
const clean = (id = 1) => ({ account_id: id, reset_at: '2026-11-12T12:00:00Z', cleared: ['quality', 'session_avoid'], kept: [{ kind: 'quota', until: null }], failed: [] })

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: true,
        AccountTableFilters: FiltersStub,
        AccountBulkActionsBar: BulkBarStub,
        Pagination: true,
        ConfirmDialog: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        Icon: true,
        RouterLink: true
      }
    }
  })
}

type View = { load: () => Promise<void>; accounts: Array<{ id: number; name: string }> }
const vm = (wrapper: ReturnType<typeof mountView>) => wrapper.vm as unknown as View
const recoverButton = (id: number) => document.querySelector<HTMLButtonElement>(`[data-test="row"][data-id="${id}"] [data-test="row-recover"]`)
const line = (id: number) => document.querySelector<HTMLElement>(`[data-test="row"][data-id="${id}"] [data-sq-recover-line]`)
const rowIds = () => Array.from(document.querySelectorAll<HTMLElement>('[data-test="row"]')).map((el) => Number(el.dataset.id))
const deferred = <T = unknown>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}
async function selectAll(wrapper: ReturnType<typeof mountView>) {
  document.querySelector<HTMLInputElement>('[data-test="row"] input')!.click()
  await flushPromises()
  await wrapper.get('[data-test="select-page"]').trigger('click')
}

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
  closeSchedDrawer()
  recoveryLines.clear()
  perms.manage = true
  document.documentElement.dataset.motion = 'reduced'
  Object.values(toast).forEach((fn) => fn.mockReset())
  listAccounts.mockReset().mockResolvedValue({ items: ROWS, total: 4, page: 1, page_size: 20, pages: 1 })
  getById.mockReset().mockImplementation(async (id: number) => ({ ...ROWS.find((a) => a.id === id), status: 'active' }))
  sched.summary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [1, 2, 3, 4].map(summaryOf) })
  sched.reset.mockReset().mockImplementation(async (id: number) => clean(id))
  sched.accountDetail.mockReset().mockReturnValue(new Promise(() => {}))
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('AccountsView — 恢复', () => {
  it('offers 恢复 on every row to managers, whatever the account state, and nothing without accounts:manage', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect([1, 2, 3, 4].map((id) => recoverButton(id) !== null)).toEqual([true, true, true, true])
    wrapper.unmount()

    perms.manage = false
    const viewer = mountView()
    await flushPromises()
    expect(document.querySelectorAll('[data-test="row-recover"]')).toHaveLength(0)
    viewer.unmount()
  })

  it('runs at once with no dialog, stays greyed until the refresh, then shows its result line under the row until closed', async () => {
    const answer = deferred()
    sched.reset.mockReturnValueOnce(answer.promise)
    const wrapper = mountView()
    await flushPromises()
    const summaries = sched.summary.mock.calls.length

    recoverButton(1)!.click()
    await flushPromises()
    expect(sched.reset.mock.calls).toEqual([[1]])
    expect(document.querySelector('.modal-content')).toBeNull()
    expect(recoverButton(1)!.disabled).toBe(true)
    recoverButton(1)!.click()
    expect(sched.reset).toHaveBeenCalledTimes(1)

    answer.resolve(clean(1))
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(1)
    expect(sched.summary.mock.calls.length).toBeGreaterThan(summaries)
    expect(recoverButton(1)!.disabled).toBe(false)
    expect(line(1)!.querySelector('[data-sq-cleared]')!.textContent).toBe('admin.schedQuality.kind.quality admin.schedQuality.kind.session_avoid')
    expect(line(1)!.querySelector('[data-sq-kept]')!.textContent).toBe('admin.schedQuality.kind.quota')
    expect(toast.showSuccess).not.toHaveBeenCalled()
    line(1)!.querySelector<HTMLElement>('[data-sq-recover-close]')!.click()
    await flushPromises()
    expect(line(1)).toBeNull()
    wrapper.unmount()
  })

  it('shows 失败 on the line for a request error and the failed items of a 200 answer, and still reads the row again', async () => {
    sched.reset
      .mockResolvedValueOnce({ ...clean(1), failed: [{ kind: 'rate_limit' }] })
      .mockRejectedValueOnce(new Error('down'))
    const wrapper = mountView()
    await flushPromises()
    recoverButton(1)!.click()
    await flushPromises()
    recoverButton(2)!.click()
    await flushPromises()
    expect(line(1)!.querySelector('[data-sq-failed]')!.textContent).toBe('admin.schedQuality.kind.rate_limit')
    expect(line(2)!.textContent).toContain('admin.schedQuality.result.failed')
    expect(line(2)!.querySelector('[data-sq-cleared]')).toBeNull()
    expect(getById.mock.calls.map(([id]) => id)).toEqual([1, 2])
    wrapper.unmount()
  })

  it('loads the list again under a quality filter, so the recovered row leaves it', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="filter-substandard"]').trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 400))
    await flushPromises()
    listAccounts.mockResolvedValue({ items: [ROWS[0]], total: 1, page: 1, page_size: 20, pages: 1 })
    const loads = listAccounts.mock.calls.length
    recoverButton(2)!.click()
    await flushPromises()
    expect(listAccounts.mock.calls.length).toBeGreaterThan(loads)
    expect(listAccounts.mock.calls.at(-1)![2]).toMatchObject({ sched_quality: 'substandard' })
    expect(rowIds()).toEqual([1])
    wrapper.unmount()
  })

  it('confirms a batch recovery once, gives every account its result line and keeps the failed accounts selected', async () => {
    sched.reset.mockImplementation(async (id: number) => {
      if (id === 3) throw new Error('down')
      return id === 4 ? { ...clean(id), failed: [{ kind: 'rate_limit' }] } : clean(id)
    })
    const wrapper = mountView()
    await flushPromises()
    await selectAll(wrapper)
    await wrapper.get('[data-test="bulk-sched-reset"]').trigger('click')
    await flushPromises()
    expect(sched.reset).not.toHaveBeenCalled()
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')!.click()
    await flushPromises()

    expect(sched.reset.mock.calls.map(([id]) => id).sort()).toEqual([1, 2, 3, 4])
    expect(document.querySelector('[data-sq-batch-results]')).toBeNull()
    expect([1, 2, 3, 4].map((id) => line(id) !== null)).toEqual([true, true, true, true])
    expect(wrapper.findComponent(BulkBarStub).props('selectedIds')).toEqual([3, 4])
    wrapper.unmount()
  })

  it('sends nothing from a batch confirmation left open once accounts:manage is withdrawn', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectAll(wrapper)
    await wrapper.get('[data-test="bulk-sched-reset"]').trigger('click')
    await flushPromises()
    expect(document.querySelector('[data-sq-batch-confirm]')).not.toBeNull()
    perms.manage = false
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')?.click()
    await flushPromises()
    expect(wrapper.findComponent(BulkBarStub).exists()).toBe(false)
    expect(sched.reset).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('AccountsView — recovery refresh', () => {
  it('keeps a row recovery locked until the summary refresh is in', async () => {
    const wrapper = mountView()
    await flushPromises()
    const summary = deferred()
    sched.summary.mockReturnValueOnce(summary.promise)
    recoverButton(2)!.click()
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(2)
    expect(recoverButton(2)!.disabled).toBe(true)
    summary.resolve({ at: '', stale: false, global_enabled: true, accounts: [1, 2, 3, 4].map(summaryOf) })
    await flushPromises()
    expect(recoverButton(2)!.disabled).toBe(false)
    expect(sched.reset).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('refreshes the summary at once when recovering from the open drawer, and holds the lock for it', async () => {
    const detail = structuredClone(detailFixture)
    detail.account = { ...detail.account, id: 2, name: 'acc-2' }
    sched.accountDetail.mockResolvedValue(detail)
    const wrapper = mountView()
    await flushPromises()
    openSchedDrawer({ id: 2, name: 'acc-2' })
    await flushPromises()
    const before = sched.summary.mock.calls.length
    const summary = deferred()
    sched.summary.mockReturnValueOnce(summary.promise)
    document.querySelector<HTMLElement>('[data-sq-trigger="drawer-reset"]')!.click()
    await flushPromises()
    expect(sched.reset).toHaveBeenCalledWith(2)
    expect(getById).toHaveBeenCalledWith(2)
    expect(sched.summary.mock.calls.length).toBe(before + 1)
    expect(document.querySelector('[data-sq-trigger="drawer-reset"]')!.getAttribute('aria-busy')).toBe('true')
    summary.resolve({ at: '', stale: false, global_enabled: true, accounts: [summaryOf(2)] })
    await flushPromises()
    expect(document.querySelector('[data-sq-trigger="drawer-reset"]')!.getAttribute('aria-busy')).toBe('false')
    wrapper.unmount()
  })

  it('drops a late row read when a newer list load landed meanwhile', async () => {
    const wrapper = mountView()
    await flushPromises()
    const stale = deferred()
    getById.mockReturnValueOnce(stale.promise)
    recoverButton(1)!.click()
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(1)
    listAccounts.mockResolvedValue({ items: ROWS.map((a) => (a.id === 1 ? { ...a, name: 'newer row', status: 'error' } : a)), total: 4, page: 1, page_size: 20, pages: 1 })
    await vm(wrapper).load()
    expect(vm(wrapper).accounts[0].name).toBe('newer row')
    stale.resolve({ ...ROWS[0], name: 'older row', status: 'active' })
    await flushPromises()
    expect(vm(wrapper).accounts[0].name).toBe('newer row')
    wrapper.unmount()
  })

  it('keeps the batch accounts locked until the list and the summary reload are in', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectAll(wrapper)
    const summary = deferred()
    sched.summary.mockReturnValue(summary.promise)
    await wrapper.get('[data-test="bulk-sched-reset"]').trigger('click')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')!.click()
    await flushPromises()
    expect(sched.reset).toHaveBeenCalledTimes(4)
    expect(wrapper.findComponent(BulkBarStub).props('recoverBusy')).toBe(true)
    expect(recoverButton(1)!.disabled).toBe(true)

    summary.resolve({ at: '', stale: false, global_enabled: true, accounts: [] })
    await flushPromises()
    expect(wrapper.findComponent(BulkBarStub).exists()).toBe(false)
    expect(recoverButton(1)!.disabled).toBe(false)
    wrapper.unmount()
  })

  it('sends one list and one summary request after a recovery under the quality filter, one summary after a batch', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="filter-substandard"]').trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 400))
    await flushPromises()
    const answer = deferred()
    sched.reset.mockReturnValueOnce(answer.promise)
    recoverButton(2)!.click()
    await flushPromises()
    const lists = listAccounts.mock.calls.length
    const summaries = sched.summary.mock.calls.length
    answer.resolve(clean(2))
    await flushPromises()
    expect(listAccounts.mock.calls.length).toBe(lists + 1)
    expect(sched.summary.mock.calls.length).toBe(summaries + 1)
    wrapper.unmount()

    const batchView = mountView()
    await flushPromises()
    await selectAll(batchView)
    const batchAnswer = deferred()
    sched.reset.mockImplementation(() => batchAnswer.promise)
    await batchView.get('[data-test="bulk-sched-reset"]').trigger('click')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')!.click()
    await flushPromises()
    const before = sched.summary.mock.calls.length
    batchAnswer.resolve(clean())
    await flushPromises()
    expect(sched.summary.mock.calls.length).toBe(before + 1)
    batchView.unmount()
  })
})
