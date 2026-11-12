import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'
import type { SchedAccountSummary } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'
import resetFixture from '@/__tests__/fixtures/schedQuality/reset.json'
import { closeSchedDrawer, closeSchedReset, useSchedLayers } from '@/composables/useSchedLayers'

const { listAccounts, getById, sched } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getById: vi.fn(),
  sched: { summary: vi.fn(), reset: vi.fn(), accountDetail: vi.fn(), groupBoard: vi.fn(), getGroupPolicy: vi.fn() }
}))

vi.mock('@/api/admin/schedQuality', () => ({ default: sched }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getById,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
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

type TipTarget = HTMLElement & { __tipText?: string }

// Renders rows in the order given, with the quality cells and the TTFT header (its page-sort button).
const DataTableStub = defineComponent({
  props: ['columns', 'data'],
  setup(props, { slots }) {
    return () => h('div', [
      h('div', { 'data-test': 'header' }, slots['header-sq_ttfo']?.({ column: { label: 'ttfo' } })),
      ...(props.data as Array<{ id: number }>).map((row) =>
        h('div', { 'data-test': 'row', 'data-id': row.id }, [
          h('span', { 'data-test': 'ttfo' }, slots['cell-sq_ttfo']?.({ row })),
          h('span', { 'data-test': 'error' }, slots['cell-sq_error']?.({ row })),
          h('span', { 'data-test': 'groups' }, slots['cell-sq_groups']?.({ row }))
        ]))
    ])
  }
})

const ActionMenuStub = defineComponent({
  props: ['show', 'account'],
  emits: ['recover-state'],
  setup(props, { emit }) {
    // The real menu emits the account it was opened for; here that is the first row.
    return () => h('button', { 'data-test': 'menu-recover', onClick: () => emit('recover-state', props.account ?? account(1, 'acc-sol')) }, 'recover')
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

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        AccountActionMenu: ActionMenuStub,
        AccountStatusIndicator: true,
        AccountTableActions: true,
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
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
        Icon: true
      }
    }
  })
}

const rowIds = () => Array.from(document.querySelectorAll<HTMLElement>('[data-test="row"]')).map((el) => Number(el.dataset.id))
const rowCell = (id: number, cell: string) => document.querySelector<TipTarget>(`[data-test="row"][data-id="${id}"] [data-test="${cell}"] button`)!

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = ''
  closeSchedDrawer()
  closeSchedReset()
  document.documentElement.dataset.motion = 'reduced'
  listAccounts.mockReset().mockResolvedValue({ items: [account(1, 'acc-sol'), account(2, 'acc-astra'), account(3, 'acc-slow'), account(4, 'acc-empty')], total: 4, page: 1, page_size: 20, pages: 1 })
  sched.summary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: summaries() })
  sched.accountDetail.mockReset().mockReturnValue(new Promise(() => {}))
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('AccountsView quality columns', () => {
  it('uses each row\'s own summary model for its numbers, tips and the drawer it opens', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(sched.summary).toHaveBeenCalledWith([1, 2, 3, 4], undefined, expect.anything())
    expect([rowCell(1, 'ttfo').textContent, rowCell(2, 'ttfo').textContent]).toEqual(['12.4s', '9.1s'])
    expect(rowCell(1, 'ttfo').__tipText).toBe('admin.schedQuality.tip.source:{"model":"gpt-6.1-sol","n":150}')
    expect(rowCell(2, 'ttfo').__tipText).toBe('admin.schedQuality.tip.source:{"model":"gpt-6-astra","n":140}')

    const { drawer } = useSchedLayers()
    rowCell(2, 'error').click()
    expect([drawer.accountId, drawer.model, drawer.groupId]).toEqual([2, 'gpt-6-astra', null])
    closeSchedDrawer()
    // A capsule opens on its own cooling model and group.
    document.querySelector<HTMLElement>('[data-test="row"][data-id="1"] .sq-capsule')!.click()
    expect([drawer.accountId, drawer.model, drawer.groupId]).toEqual([1, 'gpt-6.1-sol', 53])
    wrapper.unmount()
  })

  it('sorts only the current page by TTFT: descending, ascending, off; timeouts largest, dashes last', async () => {
    const wrapper = mountView()
    await flushPromises()
    const sort = () => document.querySelector<HTMLElement>('[data-test="sq-sort-ttfo"]')!
    expect(rowIds()).toEqual([1, 2, 3, 4])
    sort().click(); await flushPromises()
    expect(rowIds()).toEqual([3, 1, 2, 4])
    sort().click(); await flushPromises()
    expect(rowIds()).toEqual([2, 1, 3, 4])
    sort().click(); await flushPromises()
    expect(rowIds()).toEqual([1, 2, 3, 4])
    expect(listAccounts).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('runs "recover state" from the row menu through the shared reset flow and reads the row again instead of using the result', async () => {
    sched.reset.mockResolvedValue(resetFixture)
    getById.mockResolvedValue({ ...account(1, 'acc-sol-renamed'), status: 'active' })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findComponent(ActionMenuStub).get('[data-test="menu-recover"]').trigger('click')
    await flushPromises()
    expect(useSchedLayers().reset.accountId).toBe(1)

    document.querySelector<HTMLElement>('[data-sq-reset-confirm]')!.click()
    await flushPromises()
    expect(sched.reset).toHaveBeenCalledWith(1)
    expect(document.querySelector('[data-sq-reset-result]')).not.toBeNull()

    document.querySelector<HTMLElement>('[data-sq-reset-result]')!.closest('.modal-content')!.querySelector<HTMLElement>('.modal-footer button')!.click()
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(1)
    expect(document.querySelector('[data-test="row"][data-id="1"]')).not.toBeNull()
    // The row now holds the account read back, never the reset result.
    expect((wrapper.vm as unknown as { accounts: Array<{ id: number; name: string }> }).accounts.find((a) => a.id === 1)?.name).toBe('acc-sol-renamed')
    wrapper.unmount()
  })
})
