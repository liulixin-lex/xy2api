import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts } = vi.hoisted(() => ({
  listAccounts: vi.fn()
}))

// AccountsView loads the quality summary on mount; a real request would fail after the test file finishes.
vi.mock('@/api/admin/schedQuality', () => ({ backendSummary: vi.fn().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] }), default: {} }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
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
        AccountsToolbar: { template: '<div><slot name="more-actions" /></div>' },
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

  it('shows the ten columns of the redesign by default, today\'s stats included (spec 4.1)', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(tableColumns(wrapper)).toEqual([
      'select', 'name', 'status', 'schedulable', 'groups', 'sq_recent', 'iq_check', 'capacity', 'upstream_billing_rate', 'today_stats', 'actions'
    ])
    expect(localStorage.getItem('account-hidden-columns-v2')).toBeNull()
  })

  it('starts over from the defaults when only the old column settings are saved', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats', 'status']))
    localStorage.setItem('account-column-order', JSON.stringify(['notes', 'status']))
    const wrapper = mountView()
    await flushPromises()
    expect(tableColumns(wrapper).slice(0, 4)).toEqual(['select', 'name', 'status', 'schedulable'])
    expect(tableColumns(wrapper)).toContain('today_stats')
  })

  it('applies the saved column settings between the pinned columns', async () => {
    localStorage.setItem('account-hidden-columns-v2', JSON.stringify(['notes', 'proxy']))
    localStorage.setItem('account-column-order-v2', JSON.stringify([
      'last_used_at', 'status', 'schedulable', 'groups', 'sq_recent', 'iq_check', 'capacity', 'upstream_billing_rate', 'today_stats',
      'id', 'platform_type', 'priority', 'usage', 'proxy', 'rate_multiplier', 'upstream_balance', 'created_at', 'expires_at', 'notes'
    ]))
    const wrapper = mountView()
    await flushPromises()
    const columns = tableColumns(wrapper)
    expect(columns.slice(0, 4)).toEqual(['select', 'name', 'last_used_at', 'status'])
    expect(columns).toEqual(expect.arrayContaining(['id', 'created_at', 'expires_at', 'today_stats', 'rate_multiplier']))
    expect(columns).not.toContain('notes')
    expect(columns.at(-1)).toBe('actions')
  })

  it('reorders the table and saves the order when a column is dragged in the column settings', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="account-tools"]').trigger('click')
    await wrapper.get('[data-test="swap-first-two"]').trigger('click')

    expect(tableColumns(wrapper).slice(0, 4)).toEqual(['select', 'name', 'schedulable', 'status'])
    expect(JSON.parse(localStorage.getItem('account-column-order-v2') || '[]').slice(0, 2)).toEqual(['schedulable', 'status'])
  })

  it('restores both visibility and order with "restore default columns", offered only when either differs', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="account-tools"]').trigger('click')
    expect(wrapper.find('[data-test="reset-columns"]').exists()).toBe(false)

    await wrapper.get('[data-test="swap-first-two"]').trigger('click')
    expect(wrapper.find('[data-test="reset-columns"]').exists()).toBe(true)
    await wrapper.get('[data-test="reset-columns"]').trigger('click')
    expect(tableColumns(wrapper).slice(0, 4)).toEqual(['select', 'name', 'status', 'schedulable'])
    expect(localStorage.getItem('account-column-order-v2')).toBeNull()
    expect(localStorage.getItem('account-hidden-columns-v2')).toBeNull()
    expect(wrapper.find('[data-test="reset-columns"]').exists()).toBe(false)

    // A visibility that differs from the default offers it too.
    await wrapper.findAll('[data-test="column-setting"] button')[0].trigger('click')
    expect(wrapper.find('[data-test="reset-columns"]').exists()).toBe(true)
  })
})
