import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, listWithEtag, getUpstreamBalances } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getUpstreamBalances: vi.fn()
}))

// AccountsView loads the quality summary on mount; a real request would fail after the test file finishes.
vi.mock('@/api/admin/schedQuality', () => ({ backendSummary: vi.fn().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] }), default: {} }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} }),
      getUpstreamBalances,
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      getUpstreamBillingRatesWithEtag: vi.fn().mockResolvedValue({ notModified: true, etag: null, data: null }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([]) }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${Object.values(params).join(',')}` : key
    })
  }
})

// Renders the upstream balance cell plus the cells used to open a dialog or edit an account.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id">
        <div :data-test="'balance-' + row.id"><slot name="cell-upstream_balance" :row="row" /></div>
        <slot name="cell-priority" :row="row" />
        <slot name="cell-groups" :row="row" />
      </div>
    </div>
  `
}



const BulkEditAccountModalStub = {
  name: 'BulkEditAccountModal',
  props: ['show', 'target'],
  emits: ['close', 'updated'],
  template: '<div data-test="bulk-edit-modal" />'
}

const AccountGroupsDialogStub = {
  name: 'AccountGroupsDialog',
  props: ['account', 'groups'],
  emits: ['close', 'updated'],
  template: '<div data-test="groups-dialog" />'
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        AccountColumnSettings: true,
        AccountPriorityCell: true,
        AccountGroupsDialog: AccountGroupsDialogStub,
        AccountsToolbar: true,
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
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
        BulkEditAccountModal: BulkEditAccountModalStub,
        Icon: true,
        Teleport: true
      }
    }
  })
}

const NOW = Date.parse('2026-10-27T12:03:00Z')
const iso = (ms: number) => new Date(ms).toISOString()

const row = (id: number, updatedAt = '2026-10-27T10:00:00Z', extra: Record<string, unknown> = {}) => ({
  id,
  name: `acc-${id}`,
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  updated_at: updatedAt,
  proxy_id: null,
  credentials: { base_url: 'https://relay.example.com/v1', api_protocol: 'chat_completions' },
  credentials_status: { has_api_key: true },
  ...extra
})
const page = (ids: number[], pageNo = 1, total = ids.length, makeRow: (id: number) => ReturnType<typeof row> = id => row(id)) => ({
  items: ids.map(makeRow),
  total,
  page: pageNo,
  page_size: 20,
  pages: Math.max(1, Math.ceil(total / 20))
})

const okItem = (accountID: number, amount: number, observedAt = iso(NOW - 10_000)) => ({
  account_id: accountID,
  state: 'ok',
  amount_usd: amount,
  unlimited: false,
  basis: 'wallet',
  upstream_kind: 'sub2api',
  reason: null,
  observed_at: observedAt,
  last_attempt_at: observedAt
})

const answer = (...items: ReturnType<typeof okItem>[]) => ({ items, server_time: iso(NOW) })

const balanceText = (wrapper: ReturnType<typeof mountView>, id: number) =>
  wrapper.get(`[data-test="balance-${id}"] [data-testid="upstream-balance-value"]`).text()

// 卸载每个用例挂载的页面，避免它们的轮询和 visibilitychange 监听串到后面的用例。
enableAutoUnmount(afterEach)

describe('admin AccountsView upstream balance column (a 30-second source of the live table)', () => {
  beforeEach(() => {
    localStorage.clear()
    // The column is optional; these cases show it.
    localStorage.setItem('account-hidden-columns-v2', '[]')
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    vi.spyOn(console, 'error').mockImplementation(() => {})
    listAccounts.mockReset().mockImplementation(async (_page: number, _size: number, filters: { ids?: string }) =>
      filters.ids ? page(filters.ids.split(',').map(Number)) : page([1, 2]))
    getUpstreamBalances.mockReset().mockImplementation(async (ids: number[]) =>
      answer(...ids.map(id => okItem(id, id === 1 ? 12.34 : 7)))
    )
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('asks for the visible accounts after the load and every 30 seconds', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
    expect(getUpstreamBalances.mock.calls[0][0]).toEqual([1, 2])
    expect(balanceText(wrapper, 1)).toContain('12.34')

    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
  })

  it('asks again right after a page change with only the new page ids', async () => {
    const wrapper = mountView()
    await flushPromises()
    listAccounts.mockImplementation(async (_page: number, _size: number, filters: { ids?: string }) =>
      filters.ids ? page(filters.ids.split(',').map(Number)) : page([3], 2, 21))
    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenLastCalledWith([3], expect.anything())
  })

  it('does not ask while the column is hidden, and asks at once when it is shown', async () => {
    localStorage.setItem('account-hidden-columns-v2', JSON.stringify(['upstream_balance']))
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(getUpstreamBalances).not.toHaveBeenCalled()

    ;(wrapper.vm as unknown as { toggleColumn: (key: string) => void }).toggleColumn('upstream_balance')
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('asks in chunks of at most 200 ids and keeps the previous values of a chunk that failed', async () => {
    const ids = Array.from({ length: 250 }, (_, i) => i + 1)
    listAccounts.mockImplementation(async (_page: number, _size: number, filters: { ids?: string }) =>
      filters.ids ? page(filters.ids.split(',').map(Number)) : { ...page(ids), page_size: 250 })
    const wrapper = mountView()
    await flushPromises()
    expect(getUpstreamBalances.mock.calls.map(([chunk]) => chunk.length)).toEqual([200, 50])
    expect(balanceText(wrapper, 1)).toContain('12.34')

    getUpstreamBalances.mockImplementation(async (chunk: number[]) => {
      if (chunk.includes(1)) throw new Error('chunk down')
      return answer(...chunk.map(id => okItem(id, 1)))
    })
    await vi.advanceTimersByTimeAsync(30_000)
    expect(balanceText(wrapper, 1)).toContain('12.34')
  })

  it('stops asking after a 403', async () => {
    getUpstreamBalances.mockRejectedValue({ status: 403, reason: 'ADMIN_PERMISSION_DENIED' })
    mountView()
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(30_000 * 4)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('stops asking after the page is left', async () => {
    const wrapper = mountView()
    await flushPromises()
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })
})
