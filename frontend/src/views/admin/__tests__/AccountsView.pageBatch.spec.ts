import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, getBatchTodayStats } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getBatchTodayStats: vi.fn()
}))

// AccountsView loads the quality summary on mount; a real request would fail after the test file finishes.
vi.mock('@/api/admin/schedQuality', () => ({ backendSummary: vi.fn().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] }), default: {} }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getBatchTodayStats,
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      getUpstreamBalances: vi.fn().mockResolvedValue({ items: [] }),
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
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: '<div><div v-for="row in data" :key="row.id">{{ row.name }}</div></div>'
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
        AccountStatusIndicator: { props: ['account'], template: '<span data-test="status-badge">{{ account.status }}</span>' },
        AccountsToolbar: true,
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
        BulkEditAccountModal: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

// Today's stats (a partial source of the live table) run after every active load: first load, page, page size, filter.
describe('admin AccountsView per-page batches', () => {
  const row = (id: number, name: string) => ({ id, name, platform: 'openai', type: 'apikey', status: 'active', schedulable: true })

  beforeEach(() => {
    localStorage.clear()
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    listAccounts
      .mockReset()
      .mockResolvedValueOnce({ items: [row(1, 'busy'), row(2, 'quiet')], total: 3, page: 1, page_size: 2, pages: 2 })
      .mockResolvedValue({ items: [row(3, 'next')], total: 3, page: 2, page_size: 2, pages: 2 })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('asks once on the initial load and again for the new rows after a page change', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(getBatchTodayStats).toHaveBeenCalledTimes(1)
    expect(getBatchTodayStats).toHaveBeenLastCalledWith([1, 2])

    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()

    expect(listAccounts).toHaveBeenLastCalledWith(2, expect.any(Number), expect.anything(), expect.anything())
    expect(getBatchTodayStats).toHaveBeenCalledTimes(2)
    expect(getBatchTodayStats).toHaveBeenLastCalledWith([3])
  })

  it('asks again after a page-size change', async () => {
    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:pageSize', 50)
    await flushPromises()

    expect(listAccounts).toHaveBeenLastCalledWith(1, 50, expect.anything(), expect.anything())
    expect(getBatchTodayStats).toHaveBeenCalledTimes(2)
    expect(getBatchTodayStats).toHaveBeenLastCalledWith([3])
  })

  it('asks again after a filter change', async () => {
    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent({ name: 'AccountsToolbar' }).vm.$emit('change', { platform: 'openai' })
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(getBatchTodayStats).toHaveBeenCalledTimes(2)
    expect(getBatchTodayStats).toHaveBeenLastCalledWith([3])
  })

  it('skips the request while every column that shows it is hidden, and ignores unknown saved columns', async () => {
    localStorage.setItem('account-hidden-columns-v2', JSON.stringify(['today_stats', 'usage', 'retired_column']))
    mountView()
    await flushPromises()
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(getBatchTodayStats).not.toHaveBeenCalled()
  })
})
