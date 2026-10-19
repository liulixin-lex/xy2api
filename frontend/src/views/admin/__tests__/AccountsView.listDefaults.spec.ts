import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts } = vi.hoisted(() => ({
  listAccounts: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
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

const DataTableStub = {
  props: ['columns'],
  template: '<div data-test="columns">{{ columns.map(column => column.key).join(",") }}</div>'
}

// Stands in for VueDraggable: lets a test drop the first column after the second one,
// emitting a reordered copy of the list like the real component does.
const VueDraggableStub = {
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: `
    <div>
      <slot />
      <button data-test="swap-first-two" @click="$emit('update:modelValue', [modelValue[1], modelValue[0], ...modelValue.slice(2)])">swap</button>
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
        VueDraggable: VueDraggableStub,
        AccountTableActions: { template: '<div><slot name="after" /></div>' },
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
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

const tableColumns = (wrapper: ReturnType<typeof mountView>) =>
  wrapper.get('[data-test="columns"]').text().split(',')

describe('admin AccountsView list defaults and column order', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
  })

  it('lists accounts by last use, newest first, unless a sort was chosen after the change', async () => {
    // A sort saved under the old key predates the new default and no longer applies.
    localStorage.setItem('account-table-sort', JSON.stringify({ key: 'name', order: 'asc' }))
    mountView()
    await flushPromises()
    expect(listAccounts.mock.calls[0][2]).toEqual(expect.objectContaining({ sort_by: 'last_used_at', sort_order: 'desc' }))

    listAccounts.mockClear()
    localStorage.setItem('account-table-sort-v2', JSON.stringify({ key: 'priority', order: 'asc' }))
    mountView()
    await flushPromises()
    expect(listAccounts.mock.calls[0][2]).toEqual(expect.objectContaining({ sort_by: 'priority', sort_order: 'asc' }))
  })

  it('applies the saved column order between the pinned columns', async () => {
    // The column settings always save the full list; here "last used" was dragged to the top.
    localStorage.setItem('account-column-order', JSON.stringify([
      'last_used_at', 'id', 'platform_type', 'capacity', 'status', 'schedulable', 'iq_check', 'groups',
      'today_stats', 'usage', 'proxy', 'priority', 'scheduler_score', 'rate_multiplier',
      'upstream_billing_rate', 'created_at', 'expires_at', 'notes'
    ]))
    const wrapper = mountView()
    await flushPromises()

    const columns = tableColumns(wrapper)
    expect(columns.slice(0, 5)).toEqual(['select', 'name', 'last_used_at', 'id', 'platform_type'])
    expect(columns.at(-1)).toBe('actions')
  })

  it('reorders the table and saves the order when a column is dragged in the column settings', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(tableColumns(wrapper).slice(0, 4)).toEqual(['select', 'name', 'id', 'platform_type'])

    await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
    await wrapper.get('[data-test="swap-first-two"]').trigger('click')

    expect(tableColumns(wrapper).slice(0, 4)).toEqual(['select', 'name', 'platform_type', 'id'])
    expect(JSON.parse(localStorage.getItem('account-column-order') || '[]').slice(0, 2)).toEqual(['platform_type', 'id'])

    await wrapper.get('[data-test="reset-column-order"]').trigger('click')
    expect(tableColumns(wrapper).slice(0, 4)).toEqual(['select', 'name', 'id', 'platform_type'])
    expect(localStorage.getItem('account-column-order')).toBeNull()
  })
})
