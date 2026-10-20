import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, getBatchRecentHealth } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getBatchRecentHealth: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getBatchRecentHealth,
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
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
    useI18n: () => ({ t: (key: string) => key })
  }
})

// Renders only the status cell of each row.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-test="'status-' + row.id">
        <slot name="cell-status" :row="row" />
      </div>
    </div>
  `
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
        AccountTableActions: true,
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
        BulkEditAccountModal: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

describe('admin AccountsView recent health in the status column', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({
      items: [
        { id: 1, name: 'busy', platform: 'openai', type: 'apikey', status: 'active', schedulable: true },
        { id: 2, name: 'quiet', platform: 'openai', type: 'apikey', status: 'active', schedulable: true }
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getBatchRecentHealth.mockReset().mockResolvedValue({
      stats: { '1': { first_token_avg_ms: 1200, first_token_samples: 6, recent_errors: 4 } },
      first_token_window_seconds: 180,
      errors_window_seconds: 600
    })
  })

  it('asks for the visible accounts and shows errors beside and first token under the status', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getBatchRecentHealth).toHaveBeenCalledWith([1, 2])

    const busy = wrapper.get('[data-test="status-1"]')
    expect(busy.get('[data-test="status-badge"]').text()).toBe('active')
    const errors = busy.get('[data-test="recent-errors"]')
    expect(errors.text()).toBe('4')
    expect(errors.attributes('href')).toBe('/admin/usage?tab=errors&account_id=1')
    expect(errors.attributes('target')).toBe('_blank')
    expect(busy.get('[data-test="first-token-avg"]').exists()).toBe(true)

    const quiet = wrapper.get('[data-test="status-2"]')
    expect(quiet.find('[data-test="recent-errors"]').exists()).toBe(false)
    expect(quiet.find('[data-test="first-token-avg"]').exists()).toBe(false)
  })

  it('skips the request while the status column is hidden', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['status']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    mountView()
    await flushPromises()
    expect(getBatchRecentHealth).not.toHaveBeenCalled()
  })
})
