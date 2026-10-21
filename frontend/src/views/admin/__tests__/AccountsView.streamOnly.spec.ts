import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, getBatchRecentHealth, clearCapabilityLearning } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getBatchRecentHealth: vi.fn(),
  clearCapabilityLearning: vi.fn()
}))

vi.mock('@/api/admin/accountCapability', async () => {
  const actual = await vi.importActual<typeof import('@/api/admin/accountCapability')>('@/api/admin/accountCapability')
  return { ...actual, clearCapabilityLearning }
})

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
    useI18n: () => ({ t: (key: string) => key, te: () => true })
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

const learned = () => [{ protocol: 'responses', mode: 'stream_only', expires_at: new Date(Date.now() + 3 * 3_600_000).toISOString() }]

describe('admin AccountsView stream-only state in the status column', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({
      items: [
        { id: 1, name: 'learned', platform: 'openai', type: 'apikey', status: 'active', schedulable: true, capability_learned: learned() },
        { id: 2, name: 'plain', platform: 'openai', type: 'apikey', status: 'active', schedulable: true }
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getBatchRecentHealth.mockReset().mockResolvedValue({ stats: {}, first_token_window_seconds: 180, errors_window_seconds: 600 })
    clearCapabilityLearning.mockReset().mockResolvedValue(undefined)
  })

  it('shows the learned stream-only state under the status and clears it in place', async () => {
    const wrapper = mountView()
    await flushPromises()

    const row = wrapper.get('[data-test="status-1"]')
    expect(row.get('[data-test="status-badge"]').text()).toBe('active')
    expect(row.find('[data-test="stream-only-badge"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="status-2"]').find('[data-test="stream-only-badge"]').exists()).toBe(false)

    await row.get('[data-test="stream-only-clear"]').trigger('click')
    await flushPromises()
    expect(clearCapabilityLearning).toHaveBeenCalledWith(1)
    expect(wrapper.get('[data-test="status-1"]').find('[data-test="stream-only-badge"]').exists()).toBe(false)
  })
})
