import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'
import type { SchedAccountSummary } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import { closeSchedDrawer } from '@/composables/useSchedLayers'

// "Recover state" on the account list (contract delta F; acceptance scenarios 37–41).

const { listAccounts, getById, sched, toast } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  sched: { summary: vi.fn(), reset: vi.fn(), startTest: vi.fn(), accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn() },
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
      getTempUnschedulableStatus: vi.fn().mockResolvedValue({ active: false }),
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
      h('div', { 'data-test': 'row', 'data-id': row.id }, [slots['cell-select']?.({ row })])))
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

const TempUnschedStub = defineComponent({
  props: ['show', 'account', 'recovering'],
  emits: ['recover'],
  setup(props, { emit }) {
    return () => (props.show
      ? h('button', { 'data-test': 'temp-recover', disabled: props.recovering, onClick: () => emit('recover', props.account) })
      : null)
  }
})

// Turns on the "cooling" quality filter the way the filter bar does.
const FiltersStub = defineComponent({
  props: ['filters'],
  emits: ['update:filters', 'change'],
  setup(props, { emit }) {
    return () => h('button', { 'data-test': 'filter-cooling', onClick: () => { emit('update:filters', { ...props.filters, sched_quality: 'cooling' }); emit('change') } })
  }
})

const account = (id: number, patch: Record<string, unknown> = {}) =>
  ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53], ...patch })
const base = (summaryFixture as { accounts: SchedAccountSummary[] }).accounts[0]
const summaryOf = (id: number, cooling: number): SchedAccountSummary =>
  ({ ...structuredClone(base), account_id: id, groups: { total: 1, normal: 1 - cooling, cooling, nodata: 0 }, cooling: cooling ? base.cooling : [] })

// 1 error, 2 only a cooling group, 3 normal, 4 scheduling switched off (not something a recovery clears).
const ROWS = [account(1, { status: 'error' }), account(2), account(3), account(4, { schedulable: false })]
const clean = { reset_at: 'x', cleared: [{ key: 'quality', kind: 'quality' }], preserved: [{ key: 'session_avoid', kind: 'session_avoid', why: '' }], failed: [] }

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountStatusIndicator: true,
        AccountTableActions: true,
        AccountTableFilters: FiltersStub,
        AccountBulkActionsBar: BulkBarStub,
        TempUnschedStatusModal: TempUnschedStub,
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
        Icon: true
      }
    }
  })
}

type View = { openMenu: (a: unknown, e: unknown) => void; handleShowTempUnsched: (a: unknown) => void; accounts: Array<{ id: number; name: string }> }
const vm = (wrapper: ReturnType<typeof mountView>) => wrapper.vm as unknown as View
async function openMenuFor(wrapper: ReturnType<typeof mountView>, id: number) {
  vm(wrapper).openMenu(vm(wrapper).accounts.find((a) => a.id === id), { currentTarget: document.body })
  await flushPromises()
}
const recoverItem = () => document.querySelector<HTMLElement>('[data-test="menu-recover-state"]')
const rowIds = () => Array.from(document.querySelectorAll<HTMLElement>('[data-test="row"]')).map((el) => Number(el.dataset.id))
const deferred = <T = unknown>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
  closeSchedDrawer()
  perms.manage = true
  document.documentElement.dataset.motion = 'reduced'
  Object.values(toast).forEach((fn) => fn.mockReset())
  listAccounts.mockReset().mockResolvedValue({ items: ROWS, total: 4, page: 1, page_size: 20, pages: 1 })
  getById.mockReset().mockImplementation(async (id: number) => ({ ...ROWS.find((a) => a.id === id), status: 'active' }))
  sched.summary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [summaryOf(1, 0), summaryOf(2, 1), summaryOf(3, 0), summaryOf(4, 0)] })
  sched.reset.mockReset().mockResolvedValue(clean)
  sched.accountDetail.mockReset().mockReturnValue(new Promise(() => {}))
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('AccountsView — recover state', () => {
  it('offers 恢复状态 in the row menu only for a recoverable row or a cooling group in the summary (scenario 37)', async () => {
    const wrapper = mountView()
    await flushPromises()
    const offered: number[] = []
    for (const id of [1, 2, 3, 4]) {
      await openMenuFor(wrapper, id)
      if (recoverItem()) offered.push(id)
    }
    expect(offered).toEqual([1, 2])
    wrapper.unmount()
  })

  it('runs from the row menu at once: one request, one toast, no dialog; the row and the summary are read again (scenarios 38–39)', async () => {
    const answer = deferred()
    sched.reset.mockReturnValueOnce(answer.promise)
    const wrapper = mountView()
    await flushPromises()
    const summaries = sched.summary.mock.calls.length

    await openMenuFor(wrapper, 1)
    recoverItem()!.click()
    await flushPromises()
    expect(sched.reset).toHaveBeenCalledTimes(1)
    expect(sched.reset).toHaveBeenCalledWith(1)
    expect(document.querySelector('.modal-content')).toBeNull()

    // Reopened while the request runs: still first, greyed, and a click sends nothing.
    await openMenuFor(wrapper, 1)
    expect(recoverItem()!.getAttribute('aria-disabled')).toBe('true')
    recoverItem()!.click()
    // The temporary-pause dialog of the same account shares the lock.
    vm(wrapper).handleShowTempUnsched(vm(wrapper).accounts[0])
    await flushPromises()
    expect(document.querySelector<HTMLButtonElement>('[data-test="temp-recover"]')!.disabled).toBe(true)
    expect(sched.reset).toHaveBeenCalledTimes(1)

    answer.resolve(clean)
    await flushPromises()
    expect(toast.showSuccess.mock.calls).toEqual([['admin.schedQuality.recover.ok:{"name":"acc-1"}']])
    expect(toast.showError).not.toHaveBeenCalled()
    expect(getById).toHaveBeenCalledWith(1)
    expect(sched.summary.mock.calls.length).toBeGreaterThan(summaries)
    // The row holds the account read back, never the reset result, and the menu no longer offers recovery.
    await openMenuFor(wrapper, 1)
    expect(recoverItem()).toBeNull()
    expect(document.querySelector('.modal-content')).toBeNull()
    wrapper.unmount()
  })

  it('says 恢复失败 once for a 200 answer with a failed item and for a request error, and still reads the row again (scenario 39)', async () => {
    sched.reset
      .mockResolvedValueOnce({ ...clean, failed: [{ key: 'rate_limit', kind: 'rate_limit', error: 'x' }] })
      .mockRejectedValueOnce(new Error('down'))
    const wrapper = mountView()
    await flushPromises()
    await openMenuFor(wrapper, 1)
    recoverItem()!.click()
    await flushPromises()
    await openMenuFor(wrapper, 2)
    recoverItem()!.click()
    await flushPromises()
    expect(toast.showError.mock.calls).toEqual([
      ['admin.schedQuality.recover.failed:{"name":"acc-1"}'],
      ['admin.schedQuality.recover.failed:{"name":"acc-2"}']
    ])
    expect(toast.showSuccess).not.toHaveBeenCalled()
    expect(getById.mock.calls.map(([id]) => id)).toEqual([1, 2])
    wrapper.unmount()
  })

  it('runs from the temporary-pause dialog at once and closes it after the refresh (scenario 38)', async () => {
    const wrapper = mountView()
    await flushPromises()
    vm(wrapper).handleShowTempUnsched(vm(wrapper).accounts[0])
    await flushPromises()
    document.querySelector<HTMLElement>('[data-test="temp-recover"]')!.click()
    await flushPromises()
    expect(sched.reset.mock.calls).toEqual([[1]])
    expect(toast.showSuccess).toHaveBeenCalledTimes(1)
    expect(document.querySelector('[data-test="temp-recover"]')).toBeNull()
    wrapper.unmount()
  })

  it('loads the list again under the cooling filter, so the recovered row leaves it (scenario 39)', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="filter-cooling"]').trigger('click')
    await new Promise((resolve) => setTimeout(resolve, 400))
    await flushPromises()
    listAccounts.mockResolvedValue({ items: [ROWS[0]], total: 1, page: 1, page_size: 20, pages: 1 })
    getById.mockResolvedValue({ ...ROWS[1], status: 'active' })
    const loads = listAccounts.mock.calls.length
    await openMenuFor(wrapper, 2)
    recoverItem()!.click()
    await flushPromises()
    expect(listAccounts.mock.calls.length).toBeGreaterThan(loads)
    expect(listAccounts.mock.calls.at(-1)![2]).toMatchObject({ sched_quality: 'cooling' })
    expect(rowIds()).toEqual([1])
    wrapper.unmount()
  })

  it('confirms a batch recovery once, shows no result table, gives one toast with the counts and keeps the failed accounts selected (scenario 40)', async () => {
    sched.reset.mockImplementation(async (id: number) => {
      if (id === 3) throw new Error('down')
      return id === 4 ? { ...clean, failed: [{ key: 'rate_limit', kind: 'rate_limit', error: 'x' }] } : clean
    })
    const wrapper = mountView()
    await flushPromises()
    document.querySelector<HTMLInputElement>('[data-test="row"] input')!.click()
    await flushPromises()
    await wrapper.get('[data-test="select-page"]').trigger('click')
    await wrapper.get('[data-test="bulk-sched-reset"]').trigger('click')
    await flushPromises()
    expect(sched.reset).not.toHaveBeenCalled()
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')!.click()
    await flushPromises()

    expect(sched.reset.mock.calls.map(([id]) => id).sort()).toEqual([1, 2, 3, 4])
    expect(document.querySelector('[data-sq-batch-results]')).toBeNull()
    expect(toast.showError.mock.calls).toEqual([['admin.schedQuality.recover.batchPartial:{"ok":2,"failed":2}']])
    expect(toast.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.findComponent(BulkBarStub).props('selectedIds')).toEqual([3, 4])
    wrapper.unmount()
  })

  it('has no recovery entry without accounts:manage, and a batch confirmation left open sends nothing once it is withdrawn (scenario 41)', async () => {
    const wrapper = mountView()
    await flushPromises()
    document.querySelector<HTMLInputElement>('[data-test="row"] input')!.click()
    await flushPromises()
    await wrapper.get('[data-test="bulk-sched-reset"]').trigger('click')
    await flushPromises()
    expect(document.querySelector('[data-sq-batch-confirm]')).not.toBeNull()

    perms.manage = false
    await flushPromises()
    // Even a click on the confirmation as it fades out sends nothing.
    document.querySelector<HTMLElement>('[data-sq-batch-confirm]')?.click()
    await flushPromises()
    expect(wrapper.findComponent(BulkBarStub).exists()).toBe(false)
    await openMenuFor(wrapper, 1)
    expect(recoverItem()).toBeNull()
    vm(wrapper).handleShowTempUnsched(vm(wrapper).accounts[0])
    await flushPromises()
    expect(sched.reset).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
