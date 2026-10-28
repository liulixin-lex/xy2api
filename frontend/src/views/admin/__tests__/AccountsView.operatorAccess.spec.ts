import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

import AccountsView from '../AccountsView.vue'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId, AdminLevel, AdminModuleId } from '@/types/adminAccess'

const { listAccounts, getUpstreamBalances } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getUpstreamBalances: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag: vi.fn(),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getBatchRecentHealth: vi.fn().mockResolvedValue({ stats: {} }),
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
    useI18n: () => ({ t: (key: string) => key })
  }
})

// Renders the name, proxy and upstream balance cells of each row.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <div data-test="select-header"><slot name="header-select" /></div>
      <div v-for="row in data" :key="row.id">
        <div :data-test="'select-' + row.id"><slot name="cell-select" :row="row" /></div>
        <div :data-test="'name-' + row.id"><slot name="cell-name" :row="row" :value="row.name" /></div>
        <div :data-test="'proxy-' + row.id"><slot name="cell-proxy" :row="row" /></div>
        <div :data-test="'balance-' + row.id"><slot name="cell-upstream_balance" :row="row" /></div>
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
        HelpTooltip: { template: '<span><slot name="trigger" /></span>' },
        AccountColumnSettings: true,
        AccountPriorityCell: true,
        AccountGroupsCellButton: true,
        AccountGroupsDialog: true,
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
        ErrorPassthroughRulesModal: { name: 'ErrorPassthroughRulesModal', props: ['show', 'readonly'], template: '<div />' },
        TLSFingerprintProfilesModal: { name: 'TLSFingerprintProfilesModal', props: ['show', 'readonly'], template: '<div />' },
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

const NOW = Date.parse('2026-10-27T12:03:00Z')

const row = (id: number, extra: Record<string, unknown> = {}) => ({
  id,
  name: `acc-${id}`,
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  updated_at: '2026-10-27T10:00:00Z',
  proxy_id: null,
  credentials: { api_protocol: 'chat_completions' },
  credentials_status: { has_api_key: true },
  ...extra
})

const page = (rows: ReturnType<typeof row>[]) => ({
  items: rows,
  total: rows.length,
  page: 1,
  page_size: 20,
  pages: 1
})

function signIn(role: 'admin' | 'operator') {
  getActivePinia()!.state.value.auth = { user: { id: 7, role }, runMode: 'standard' }
}

function seedOperator(modules: Partial<Record<AdminModuleId, AdminLevel>>, flags: AdminFlagId[] = []) {
  signIn('operator')
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules,
    flags,
    settings: {
      ops_monitoring_enabled: false,
      ops_realtime_monitoring_enabled: false,
      ops_query_mode_default: 'auto',
      admin_usage_cache_hit_rate_enabled: false,
      admin_usage_token_speed_enabled: false,
      payment_enabled: false,
      account_quota_notify_enabled: false,
      web_search_emulation_enabled: false,
      payment_subscription_usd_to_cny_rate: 0,
      payment_recharge_fee_rate: 0
    },
    custom_menu_items: []
  }
  access.loaded = true
}

const toolsMenuText = async (wrapper: ReturnType<typeof mountView>) => {
  await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
  return wrapper.text()
}

enableAutoUnmount(afterEach)

describe('admin AccountsView for operators', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    vi.spyOn(console, 'error').mockImplementation(() => {})
    listAccounts.mockReset().mockResolvedValue(page([row(1), row(2)]))
    getUpstreamBalances.mockReset().mockImplementation(async (ids: number[]) => ({
      items: ids.map(id => ({
        account_id: id,
        state: 'ok',
        amount_usd: 5,
        unlimited: false,
        basis: 'wallet',
        upstream_kind: 'sub2api',
        reason: null,
        observed_at: new Date(NOW - 10_000).toISOString(),
        last_attempt_at: new Date(NOW - 10_000).toISOString()
      })),
      server_time: new Date(NOW).toISOString()
    }))
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it.each(['view', 'manage', 'admin'] as const)('only enables bulk selection for managers and super admins (%s)', async (level) => {
    if (level === 'admin') signIn('admin')
    else seedOperator({ accounts: level })
    const wrapper = mountView()
    await flushPromises()

    const selection = wrapper.findAll<HTMLInputElement>('[data-test^="select-"] input[type="checkbox"]')
    expect(selection).toHaveLength(3)
    expect(selection.every(input => input.element.disabled === (level === 'view'))).toBe(true)
  })

  it('shows the account name as plain text when base_url is not returned', async () => {
    seedOperator({ accounts: 'view' })
    listAccounts.mockResolvedValue(page([
      row(1),
      row(2, { credentials: { base_url: 'https://relay.example.com/v1', api_protocol: 'chat_completions' } })
    ]))
    const wrapper = mountView()
    await flushPromises()

    const plain = wrapper.get('[data-test="name-1"]')
    expect(plain.find('a').exists()).toBe(false)
    expect(plain.text()).toContain('acc-1')

    // A flag holder gets base_url back and the name links to the upstream homepage.
    const link = wrapper.get('[data-test="name-2"] a')
    expect(link.attributes('href')).toBe('https://relay.example.com')
    expect(link.text()).toBe('acc-2')
  })

  it('stops polling upstream balances after a 403', async () => {
    seedOperator({ accounts: 'view' })
    getUpstreamBalances.mockReset().mockRejectedValue({ status: 403, reason: 'ADMIN_PERMISSION_DENIED' })
    mountView()
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(30_000 * 4)

    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('hides data import, export and CRS sync from operators', async () => {
    seedOperator({ accounts: 'manage' }, ['accounts.upstream_address', 'users.balance_adjust', 'data.export'])
    const operatorText = await toolsMenuText(mountView())

    expect(operatorText).not.toContain('admin.accounts.syncFromCrs')
    expect(operatorText).not.toContain('admin.accounts.dataImport')
    expect(operatorText).not.toContain('admin.accounts.dataExport')
    expect(operatorText).toContain('admin.errorPassthrough.title')

    useAdminAccessStore().reset()
    signIn('admin')
    const adminText = await toolsMenuText(mountView())

    expect(adminText).toContain('admin.accounts.syncFromCrs')
    expect(adminText).toContain('admin.accounts.dataImport')
    expect(adminText).toContain('admin.accounts.dataExport')
  })

  it('opens the account tool panels read-only for view-only operators', async () => {
    const openPanels = async () => {
      const wrapper = mountView()
      await flushPromises()
      for (const title of ['admin.errorPassthrough.title', 'admin.tlsFingerprintProfiles.title']) {
        await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
        await wrapper.findAll('button.account-tools-menu-item').find(button => button.text().includes(title))!.trigger('click')
        await flushPromises()
      }
      const readonlyOf = (name: string) => wrapper.findComponent({ name }).props('readonly')
      const result = [readonlyOf('ErrorPassthroughRulesModal'), readonlyOf('TLSFingerprintProfilesModal')]
      wrapper.unmount()
      return result
    }

    seedOperator({ accounts: 'view' })
    expect(await openPanels()).toEqual([true, true])

    seedOperator({ accounts: 'manage' })
    expect(await openPanels()).toEqual([false, false])
  })

  it('hides revert proxy fallback from operators', async () => {
    const fallbackRow = row(1, {
      proxy_id: 3,
      proxy: { id: 3, name: 'fallback-proxy' },
      proxy_fallback_origin_id: 2,
      proxy_fallback_origin_name: 'origin-proxy'
    })
    listAccounts.mockResolvedValue(page([fallbackRow]))

    seedOperator({ accounts: 'manage' }, ['accounts.upstream_address'])
    const operatorView = mountView()
    await flushPromises()
    expect(operatorView.get('[data-test="proxy-1"]').text()).toContain('admin.accounts.fallbackActive')
    expect(operatorView.find('[data-testid="revert-proxy-fallback"]').exists()).toBe(false)
    operatorView.unmount()

    signIn('admin')
    const adminView = mountView()
    await flushPromises()
    expect(adminView.find('[data-testid="revert-proxy-fallback"]').exists()).toBe(true)
  })
})
