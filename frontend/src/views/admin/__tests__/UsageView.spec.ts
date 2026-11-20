const { submitExport } = vi.hoisted(() => ({ submitExport: vi.fn().mockResolvedValue(undefined) }))
vi.mock('@/components/usage/UsageExportTasks.vue', async () => {
 const { defineComponent } = await import('vue')
 return { default: defineComponent({ props: ['scope'], setup(_, { expose }) { expose({ create: submitExport }); return {} }, template: '<div />' }) }
})
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'

import UsageView from '../UsageView.vue'

const metricSettings = vi.hoisted(() => ({ usageCacheHitRateEnabled: true, usageTokenSpeedEnabled: true, fetch: vi.fn() }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => metricSettings }))

const { list, exportList, getStats, getSnapshotV2, getById, getAccountById, getModelStats, listErrorLogs, routeQuery, saveAs } = vi.hoisted(() => {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
  })

  return {
    list: vi.fn(),
		exportList: vi.fn(),
    getStats: vi.fn(),
    getSnapshotV2: vi.fn(),
    getById: vi.fn(),
    getAccountById: vi.fn(),
    getModelStats: vi.fn(),
    listErrorLogs: vi.fn(),
    routeQuery: {} as Record<string, string>,
		saveAs: vi.fn(),
  }
})

const messages: Record<string, string> = {
  'admin.dashboard.timeRange': 'Time Range',
  'admin.dashboard.day': 'Day',
  'admin.dashboard.hour': 'Hour',
  'admin.usage.failedToLoadUser': 'Failed to load user',
	'admin.usage.requestId': 'Request ID',
	'admin.usage.upstreamRequestId': 'Upstream ID',
	'usage.requestedModel': 'Requested model',
	'usage.sentUpstreamModel': 'Sent upstream model',
	'usage.upstreamResponseModel': 'Upstream response model',
	'usage.upstreamModelMismatch': 'Upstream model mismatch',
	'common.yes': 'Yes',
	'common.no': 'No',
}

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      list,
      getStats,
    },
    dashboard: {
      getSnapshotV2,
      getModelStats,
    },
    users: {
      getById,
    },
    accounts: {
      getById: getAccountById,
    },
  },
}))

vi.mock('@/api/admin/usage', () => ({
  adminUsageAPI: {
		list: exportList,
  },
}))

vi.mock('file-saver', () => ({ saveAs }))

vi.mock('@/api/admin/ops', () => ({
  listErrorLogs,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showWarning: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/utils/format', () => ({
  formatReasoningEffort: (value: string | null | undefined) => value ?? '-',
  formatUsdTotal: (value: number | null | undefined) => `$${value ?? 0}`,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({
    query: routeQuery
  })
}))

const AppLayoutStub = { template: '<div><slot /></div>' }
const UsageFiltersStub = defineComponent({
  setup(_, { expose }) {
    const userKeyword = ref('')
    let userSearchRevision = 0
    const setUserKeyword = (email: string) => {
      userSearchRevision += 1
      userKeyword.value = email
    }
    const accountKeyword = ref('')
    expose({
      getUserSearchRevision: () => userSearchRevision,
      setUserKeyword,
      simulateUserInput: setUserKeyword,
      setAccountKeyword: (name: string) => { accountKeyword.value = name },
    })
    return { userKeyword, accountKeyword }
  },
  template: '<div><span data-test="user-filter-label">{{ userKeyword }}</span><span data-test="account-filter-label">{{ accountKeyword }}</span><slot name="status" /><slot name="after-reset" /><div data-test="phone-tools"><slot name="phone-tools" /></div><div data-test="info"><slot name="info" /></div></div>',
})
const UsageTableStub = {
  props: ['columns'],
  emits: ['userClick'],
  template: '<div data-test="usage-table"><button class="user-click" @click="$emit(\'userClick\', 2)">user</button></div>',
}
const UserTokenRankingStub = {
  emits: ['select-user'],
  template: '<div data-test="ranking"><button class="pick-user" @click="$emit(\'select-user\', 5, \'rank@test.com\')">pick</button></div>',
}
const ModelDistributionChartStub = {
  props: ['metric'],
  emits: ['update:metric'],
  template: `
    <div data-test="model-chart">
      <span class="metric">{{ metric }}</span>
      <button class="switch-metric" @click="$emit('update:metric', 'actual_cost')">switch</button>
    </div>
  `,
}
const GroupDistributionChartStub = {
  props: ['metric'],
  emits: ['update:metric'],
  template: `
    <div data-test="group-chart">
      <span class="metric">{{ metric }}</span>
      <button class="switch-metric" @click="$emit('update:metric', 'actual_cost')">switch</button>
    </div>
  `,
}

const mountRouteFilteredUsageView = () => mount(UsageView, {
  global: { stubs: {
    AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
    UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
    UserBalanceHistoryModal: true, Pagination: true, Select: true,
    DateRangePicker: true, Icon: true, TokenUsageTrend: true,
    ModelDistributionChart: true, GroupDistributionChart: true,
    EndpointDistributionChart: true, UserTokenRanking: true,
  } },
})

describe('admin UsageView route filters', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    Object.keys(routeQuery).forEach((key) => delete routeQuery[key])
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
    getById.mockReset()
  })

  afterEach(() => {
    Object.keys(routeQuery).forEach((key) => delete routeQuery[key])
    vi.useRealTimers()
  })

  it.each([[true,true],[true,false],[false,true],[false,false]])('passes display settings only through the admin page: %s / %s', async (cache, speed) => {
    metricSettings.usageCacheHitRateEnabled = cache
    metricSettings.usageTokenSpeedEnabled = speed
    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()
    const table = wrapper.findComponent({ name: 'UsageTable' })
    expect(table.props('showCacheHitRate')).toBe(cache)
    expect(table.props('showTokenSpeed')).toBe(speed)
    expect(metricSettings.fetch).toHaveBeenCalledWith(true)
    wrapper.unmount()
  })

  it('asks the usage table to show the availability fallback origin on the admin page', async () => {
    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()
    expect(wrapper.findComponent({ name: 'UsageTable' }).props('showFallbackOrigin')).toBe(true)
    wrapper.unmount()
  })

  it('shows the routed user while applying user_id to usage requests', async () => {
    routeQuery.user_id = '42'
    getById.mockResolvedValue({ id: 42, email: 'route-user@test.com' })

    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(42, true)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ user_id: 42 }), expect.anything())
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('route-user@test.com')
  })

  it('opens the error requests tab filtered to the account linked from the accounts list', async () => {
    routeQuery.tab = 'errors'
    routeQuery.account_id = '7'
    getAccountById.mockReset().mockResolvedValue({ id: 7, name: 'busy-account' })
    listErrorLogs.mockReset().mockResolvedValue({ items: [], total: 0 })

    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()

    expect((wrapper.vm as any).activeTab).toBe('errors')
    expect(listErrorLogs).toHaveBeenCalledWith(expect.objectContaining({ view: 'all', account_id: 7 }))
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ account_id: 7 }), expect.anything())
    expect(getAccountById).toHaveBeenCalledWith(7)
    expect(wrapper.find('[data-test="account-filter-label"]').text()).toBe('busy-account')
  })

  it('does not apply a stale routed user label after user_id changes', async () => {
    routeQuery.user_id = '42'
    let resolveLookup!: (user: { id: number; email: string }) => void
    getById.mockReturnValue(new Promise((resolve) => { resolveLookup = resolve }))

    const wrapper = mountRouteFilteredUsageView()
    await wrapper.vm.$nextTick()
    ;(wrapper.vm as any).filters.user_id = 84
    ;(wrapper.findComponent(UsageFiltersStub).vm as any).setUserKeyword('current-user@test.com')

    resolveLookup({ id: 42, email: 'stale-user@test.com' })
    await flushPromises()

    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('current-user@test.com')
  })

  it('does not overwrite newer user input when the routed user lookup succeeds', async () => {
    routeQuery.user_id = '42'
    let resolveLookup!: (user: { id: number; email: string }) => void
    getById.mockReturnValue(new Promise((resolve) => { resolveLookup = resolve }))

    const wrapper = mountRouteFilteredUsageView()
    await wrapper.vm.$nextTick()
    ;(wrapper.findComponent(UsageFiltersStub).vm as any).simulateUserInput('new-search@test.com')

    resolveLookup({ id: 42, email: 'route-user@test.com' })
    await flushPromises()

    expect((wrapper.vm as any).filters.user_id).toBe(42)
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('new-search@test.com')
  })

  it('does not overwrite newer user input when the routed user lookup fails', async () => {
    routeQuery.user_id = '42'
    let rejectLookup!: (error: Error) => void
    getById.mockReturnValue(new Promise((_, reject) => { rejectLookup = reject }))

    const wrapper = mountRouteFilteredUsageView()
    await wrapper.vm.$nextTick()
    ;(wrapper.findComponent(UsageFiltersStub).vm as any).simulateUserInput('new-search@test.com')

    rejectLookup(new Error('lookup failed'))
    await flushPromises()

    expect((wrapper.vm as any).filters.user_id).toBe(42)
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('new-search@test.com')
  })

  it('shows the routed user ID when its label lookup fails', async () => {
    routeQuery.user_id = '42'
    getById.mockRejectedValue(new Error('lookup failed'))

    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()

    expect(list).toHaveBeenCalledWith(expect.objectContaining({ user_id: 42 }), expect.anything())
    expect(wrapper.find('[data-test="user-filter-label"]').text()).toBe('42')
  })
})

// Mobile list plan: below 1024px the cards and charts start collapsed, and the date range, still on the page, keeps
// filtering the list.
describe('admin UsageView below 1024px', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
      matches: false, media: query, onchange: null,
      addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
    }) as unknown as MediaQueryList)
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({ total_requests: 0, total_input_tokens: 0, total_output_tokens: 0, total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0 })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })
  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('mounts no cards or charts until expanded, and the date range still reloads the list', async () => {
    const wrapper = mountRouteFilteredUsageView()
    await flushPromises()
    expect(wrapper.findComponent({ name: 'UsageStatsCards' }).exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'ModelDistributionChart' }).exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'TokenUsageTrend' }).exists()).toBe(false)

    list.mockClear()
    wrapper.findComponent({ name: 'DateRangePicker' }).vm.$emit('change', { startDate: '2026-11-01', endDate: '2026-11-02', preset: null })
    await flushPromises()
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ start_date: '2026-11-01', end_date: '2026-11-02' }), expect.anything())

    await wrapper.get('[data-testid="usage-overview-toggle"]').trigger('click')
    expect(wrapper.findComponent({ name: 'UsageStatsCards' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'ModelDistributionChart' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'ModelDistributionChart' }).props('startDate')).toBe('2026-11-01')
    wrapper.unmount()
  })
})

// Mobile list plan (B): the usage columns are remembered twice, below 768px (phone defaults) and from 768px (the page's
// existing key and its version chain); the errors tab keeps its own keys the same way.
describe('admin UsageView columns per width', () => {
  const setPhone = (phone: boolean) => vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
    matches: query.includes('min-width') ? !phone : true, media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) as unknown as MediaQueryList)
  const mountView = () => mount(UsageView, {
    global: { stubs: {
      AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub, UsageTable: true,
      UsageExportProgress: true, UsageCleanupDialog: true, UserBalanceHistoryModal: true, Pagination: true, Select: true,
      DateRangePicker: true, Icon: true, TokenUsageTrend: true, ModelDistributionChart: true, GroupDistributionChart: true,
      EndpointDistributionChart: true, UserTokenRanking: true, Teleport: true, OpsErrorLogTable: true,
    } },
  })
  const keys = (w: ReturnType<typeof mountView>) => w.findComponent({ name: 'UsageTable' }).props('columns').map((c: { key: string }) => c.key)

  // This file stubs localStorage with mocks; back them with a map here.
  const store = new Map<string, string>()
  beforeEach(() => {
    vi.useFakeTimers()
    store.clear()
    vi.mocked(localStorage.getItem).mockImplementation((key: string) => store.get(key) ?? null)
    vi.mocked(localStorage.setItem).mockImplementation((key: string, value: string) => { store.set(key, String(value)) })
    vi.mocked(localStorage.removeItem).mockImplementation((key: string) => { store.delete(key) })
    list.mockReset().mockResolvedValue({ items: [], total: 1234, pages: 62 })
    getStats.mockReset().mockResolvedValue({ total_requests: 0, total_input_tokens: 0, total_output_tokens: 0, total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0 })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })
  afterEach(() => {
    vi.mocked(window.matchMedia).mockRestore?.()
    vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null)
    vi.mocked(localStorage.setItem).mockReset()
    vi.mocked(localStorage.removeItem).mockReset()
    vi.useRealTimers()
  })

  it('shows the phone defaults below 768px and keeps a change there in the phone memory only', async () => {
    setPhone(true)
    const w = mountView()
    await flushPromises()
    expect(keys(w)).toEqual(['user', 'account', 'model', 'tokens', 'cost', 'latency', 'created_at'])
    expect(w.get('[data-test="info"]').text()).toBe('common.table.count')
    const phoneTools = w.get('[data-test="phone-tools"]')
    await phoneTools.get('[data-testid="usage-column-settings"]').trigger('click')
    await phoneTools.get('[data-testid="usage-column-toggle-group"]').trigger('change')
    expect(keys(w)).toContain('group')
    expect(JSON.parse(localStorage.getItem('usage-hidden-columns-narrow')!)).not.toContain('group')
    expect(localStorage.getItem('usage-hidden-columns')).toBeNull()
    await phoneTools.get('[data-testid="usage-column-reset"]').trigger('click')
    expect(keys(w)).not.toContain('group')
    expect(localStorage.getItem('usage-hidden-columns-narrow')).toBeNull()
    w.unmount()
  })

  // Review item 7: each tab's headers follow the sort the page requests with; hiding its column names it in the toolbar.
  it('passes each tab\'s own sort to its table and names it while its column is hidden', async () => {
    setPhone(false)
    listErrorLogs.mockReset().mockResolvedValue({ items: [], total: 0 })
    const w = mountView()
    await flushPromises()
    const usage = w.findComponent({ name: 'UsageTable' })
    usage.vm.$emit('sort', 'model', 'asc')
    await flushPromises()
    expect([usage.props('defaultSortKey'), usage.props('defaultSortOrder')]).toEqual(['model', 'asc'])
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ sort_by: 'model', sort_order: 'asc' }), expect.anything())
    expect(w.find('[data-test="table-sort-hint"]').exists()).toBe(false)
    ;(w.vm as any).toggleCurrentColumn('model')
    await flushPromises()
    expect(w.get('[data-test="info"]').find('[data-test="table-sort-hint"]').exists()).toBe(true)
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ sort_by: 'model', sort_order: 'asc' }), expect.anything())

    ;(w.vm as any).switchTab('errors')
    await flushPromises()
    expect(w.find('[data-test="table-sort-hint"]').exists()).toBe(false)
    const errors = w.findComponent({ name: 'OpsErrorLogTable' })
    errors.vm.$emit('sort', 'model', 'asc')
    await flushPromises()
    expect([errors.props('sortBy'), errors.props('sortOrder')]).toEqual(['model', 'asc'])
    expect(listErrorLogs).toHaveBeenLastCalledWith(expect.objectContaining({ sort_by: 'model', sort_order: 'asc' }))
    ;(w.vm as any).toggleCurrentColumn('model')
    await flushPromises()
    expect(w.get('[data-test="info"]').find('[data-test="table-sort-hint"]').exists()).toBe(true)
    w.unmount()
  })

  it('reads the existing key from 768px, its version chain included, and leaves it alone on a phone', async () => {
    localStorage.setItem('usage-hidden-columns', JSON.stringify(['endpoint']))
    localStorage.setItem('usage-hidden-columns-version', 'request-id-hidden-by-default')
    setPhone(false)
    const wide = mountView()
    await flushPromises()
    // A saved set is the whole hidden set: only endpoint and, from the chain, the upstream ID are hidden.
    expect(keys(wide)).toEqual(['user', 'api_key', 'account', 'model', 'reasoning_effort', 'group', 'stream', 'billing_mode', 'tokens', 'cost', 'latency', 'created_at', 'request_id', 'user_agent', 'ip_address'])
    // The chain hid the column added since (upstream ID) and wrote it back once.
    expect(JSON.parse(localStorage.getItem('usage-hidden-columns')!)).toEqual(['endpoint', 'upstream_request_id'])
    wide.unmount()
    vi.mocked(window.matchMedia).mockRestore()

    setPhone(true)
    const narrow = mountView()
    await flushPromises()
    expect(keys(narrow)).toEqual(['user', 'account', 'model', 'tokens', 'cost', 'latency', 'created_at'])
    expect(JSON.parse(localStorage.getItem('usage-hidden-columns')!)).toEqual(['endpoint', 'upstream_request_id'])
    narrow.unmount()
  })
})

describe('admin UsageView native compaction filter', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // Review item 2: the user-wait card follows the current filter's stats, never the previous filter's.
  it('marks the stats as the current filter\'s only after they load for it, and as failed when they do not', async () => {
    getStats.mockResolvedValue({ total_requests: 12, e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 })
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    const cards = () => wrapper.findComponent({ name: 'UsageStatsCards' })
    expect(cards().props('e2eState')).toBe('loaded')
    expect(cards().props('stats')).toMatchObject({ e2e_first_output_samples: 12 })

    getStats.mockRejectedValueOnce(new Error('down'))
    ;(wrapper.vm as any).filters.group_id = 54
    await wrapper.vm.$nextTick()
    expect(cards().props('e2eState')).toBe('loading')
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()
    expect(getStats).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 54 }))
    expect(cards().props('e2eState')).toBe('failed')

    getStats.mockResolvedValueOnce({ total_requests: 3, e2e_first_output_p50_ms: 9_000, e2e_first_output_samples: 3 })
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect(cards().props('e2eState')).toBe('loaded')
    expect(cards().props('stats')).toMatchObject({ e2e_first_output_samples: 3 })
    wrapper.unmount()
  })

  // Second review item 2: the list keeps failing for a new filter, so its stats are never read: the card says so.
  it('says the stats were not read while the current filter\'s list fails, and loads them once the list does', async () => {
    getStats.mockResolvedValue({ total_requests: 12, e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 })
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    const cards = () => wrapper.findComponent({ name: 'UsageStatsCards' })
    expect(cards().props('e2eState')).toBe('loaded')

    list.mockRejectedValue(new Error('down'))
    getStats.mockClear().mockResolvedValue({ total_requests: 56, e2e_first_output_p50_ms: 4_000, e2e_first_output_samples: 56 })
    ;(wrapper.vm as any).filters.group_id = 56
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(cards().props('e2eState')).toBe('blocked')

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect(getStats).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 56 }))
    expect(cards().props('e2eState')).toBe('loaded')
    expect(cards().props('stats')).toMatchObject({ e2e_first_output_samples: 56 })
    wrapper.unmount()
  })

  // Second review item 2: going back to a filter whose stats failed, the new request reads as loading, not failed.
  it('shows loading, not the earlier failure, while a filter\'s stats are asked again', async () => {
    getStats.mockResolvedValue({ total_requests: 12, e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 })
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    const cards = () => wrapper.findComponent({ name: 'UsageStatsCards' })
    const setGroup = async (id: number) => {
      ;(wrapper.vm as any).filters.group_id = id
      ;(wrapper.vm as any).applyFilters()
      await flushPromises()
    }
    getStats.mockRejectedValueOnce(new Error('down'))
    await setGroup(54)
    expect(cards().props('e2eState')).toBe('failed')
    // The engine's own retry of the failed stats is a new request too: loading while it is out.
    let retried!: (value: unknown) => void
    getStats.mockReturnValueOnce(new Promise((_, reject) => { retried = reject }))
    const asked = getStats.mock.calls.length
    await vi.advanceTimersByTimeAsync(30_000)
    expect(getStats.mock.calls.length).toBe(asked + 1)
    expect(cards().props('e2eState')).toBe('loading')
    retried(new Error('down again'))
    await flushPromises()
    expect(cards().props('e2eState')).toBe('failed')
    getStats.mockResolvedValueOnce({ total_requests: 55, e2e_first_output_p50_ms: 5_000, e2e_first_output_samples: 55 })
    await setGroup(55)
    expect(cards().props('e2eState')).toBe('loaded')

    let answer!: (value: unknown) => void
    getStats.mockReturnValueOnce(new Promise((resolve) => { answer = resolve }))
    await setGroup(54)
    expect(getStats).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 54 }))
    expect(cards().props('e2eState')).toBe('loading')
    answer({ total_requests: 54, e2e_first_output_p50_ms: 6_000, e2e_first_output_samples: 54 })
    await flushPromises()
    expect(cards().props('e2eState')).toBe('loaded')
    expect(cards().props('stats')).toMatchObject({ e2e_first_output_samples: 54 })
    wrapper.unmount()
  })

  // Third review item 1: on the same filter, a retry from a failure reads loading, never the answer shown before it.
  it('never shows a filter\'s earlier answer again after its refresh failed, until a new answer comes', async () => {
    getStats.mockResolvedValue({ total_requests: 12, e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 })
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    const cards = () => wrapper.findComponent({ name: 'UsageStatsCards' })
    const refresh = async () => { (wrapper.vm as any).refreshData(); await flushPromises() }
    expect(cards().props('e2eState')).toBe('loaded')

    getStats.mockRejectedValueOnce(new Error('down'))
    await refresh()
    expect(cards().props('e2eState')).toBe('failed')

    let fail!: (error: unknown) => void
    getStats.mockReturnValueOnce(new Promise((_, reject) => { fail = reject }))
    await refresh()
    expect(cards().props('e2eState')).toBe('loading')
    fail(new Error('down again'))
    await flushPromises()
    expect(cards().props('e2eState')).toBe('failed')

    list.mockRejectedValue(new Error('list down'))
    await refresh()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(cards().props('e2eState')).toBe('blocked')

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({ total_requests: 20, e2e_first_output_p50_ms: 8_000, e2e_first_output_samples: 20 })
    await refresh()
    expect(cards().props('e2eState')).toBe('loaded')
    expect(cards().props('stats')).toMatchObject({ e2e_first_output_samples: 20 })
    wrapper.unmount()
  })

  // Third review item 2: a cancelled list request of the same filter, ending late, changes nothing.
  it.each([
    ['a late success after a newer failure', 'resolve', false, 'blocked'],
    ['a late failure after a newer success', 'reject', true, 'loaded']
  ] as const)('keeps the state of the newer list request on %s', async (_, late, newerSucceeds, expected) => {
    getStats.mockResolvedValue({ total_requests: 12, e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 })
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    const cards = () => wrapper.findComponent({ name: 'UsageStatsCards' })
    getStats.mockRejectedValueOnce(new Error('down'))
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect(cards().props('e2eState')).toBe('failed')

    let settleOld!: { resolve: (value: unknown) => void; reject: (error: unknown) => void }
    list.mockReturnValueOnce(new Promise((resolve, reject) => { settleOld = { resolve, reject } }))
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    if (newerSucceeds) list.mockResolvedValueOnce({ items: [], total: 0, pages: 0 })
    else list.mockRejectedValueOnce(new Error('list down'))
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect(cards().props('e2eState')).toBe(expected)
    const statsCalls = getStats.mock.calls.length

    if (late === 'resolve') settleOld.resolve({ items: [], total: 0, pages: 0 })
    else settleOld.reject(new Error('old list down'))
    await flushPromises()
    expect(cards().props('e2eState')).toBe(expected)
    expect(getStats.mock.calls.length).toBe(statsCalls)
    wrapper.unmount()
  })

  // Fourth review: the 5 s membership check shares the list request but starts no stats; its failure is no block.
  it('keeps loading, then the stats failure, when the background membership check fails meanwhile', async () => {
    getStats.mockResolvedValue({ total_requests: 12, e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 })
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    const cards = () => wrapper.findComponent({ name: 'UsageStatsCards' })
    let fail!: (error: unknown) => void
    getStats.mockReturnValueOnce(new Promise((_, reject) => { fail = reject }))
    ;(wrapper.vm as any).filters.group_id = 54
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()
    expect(getStats).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 54 }))
    expect(cards().props('e2eState')).toBe('loading')
    const statsCalls = getStats.mock.calls.length

    list.mockRejectedValue(new Error('membership down'))
    const listCalls = list.mock.calls.length
    await vi.advanceTimersByTimeAsync(5_000)
    expect(list.mock.calls.length).toBeGreaterThan(listCalls)
    expect(cards().props('e2eState')).toBe('loading')

    fail(new Error('stats down'))
    await flushPromises()
    expect(cards().props('e2eState')).toBe('failed')
    const afterFailure = list.mock.calls.length
    await vi.advanceTimersByTimeAsync(5_000)
    expect(list.mock.calls.length).toBeGreaterThan(afterFailure)
    expect(cards().props('e2eState')).toBe('failed')
    expect(getStats.mock.calls.length).toBe(statsCalls)
    wrapper.unmount()
  })

  it('propagates the filter to list/stats/model/snapshot requests and clears it on reset', async () => {
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()

    list.mockClear()
    getStats.mockClear()
    getModelStats.mockClear()
    getSnapshotV2.mockClear()

    ;(wrapper.vm as any).filters.native_compaction_v2 = true
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()

    expect((wrapper.vm as any).breakdownFilters.native_compaction_v2).toBe(true)
    expect(list).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: true }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(getModelStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))

    list.mockClear()
    getStats.mockClear()
    getModelStats.mockClear()
    getSnapshotV2.mockClear()

    ;(wrapper.vm as any).resetFilters()
    await flushPromises()

    expect((wrapper.vm as any).filters.native_compaction_v2).toBeNull()
    expect((wrapper.vm as any).breakdownFilters).not.toHaveProperty('native_compaction_v2')
    expect(list).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: null }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
    expect(getModelStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }))
  })
})

describe('admin UsageView distribution metric toggles', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getById.mockReset()
    getModelStats.mockReset()

    list.mockResolvedValue({
      items: [],
      total: 0,
      pages: 0,
    })
    getStats.mockResolvedValue({
      total_requests: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_tokens: 0,
      total_tokens: 0,
      total_cost: 0,
      total_actual_cost: 0,
      average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({
      trend: [],
      models: [],
      groups: [],
    })
    getModelStats.mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps previous model stats visible during refresh until new data arrives', async () => {
    // 首次加载返回 A
    getModelStats.mockResolvedValueOnce({ models: [{ model: 'A', total_tokens: 10 }] })

    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, AuditLogModal: true, Pagination: true, Select: true,
        DateRangePicker: true, Icon: true, TokenUsageTrend: true,
        ModelDistributionChart: ModelDistributionChartStub, GroupDistributionChart: GroupDistributionChartStub,
        EndpointDistributionChart: true, UserTokenRanking: true,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()
    expect((wrapper.vm as any).requestedModelStats).toEqual([{ model: 'A', total_tokens: 10 }])

    // 刷新:让第二次 getModelStats 处于 pending,断言旧数据 A 仍在(不被清空成 [])
    let resolveSecond: (v: any) => void = () => {}
    getModelStats.mockReturnValueOnce(new Promise((res) => { resolveSecond = res }))
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect((wrapper.vm as any).requestedModelStats).toEqual([{ model: 'A', total_tokens: 10 }])

    // 新数据到达后替换为 B
    resolveSecond({ models: [{ model: 'B', total_tokens: 20 }] })
    await flushPromises()
    expect((wrapper.vm as any).requestedModelStats).toEqual([{ model: 'B', total_tokens: 20 }])
  })

  it('keeps model and group metric toggles independent without refetching chart data', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: true,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: ModelDistributionChartStub,
          GroupDistributionChart: GroupDistributionChartStub,
          UserTokenRanking: true,
        },
      },
    })

    vi.advanceTimersByTime(120)
    await flushPromises()

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      start_date: formatLocalDate(yesterday),
      end_date: formatLocalDate(now),
      granularity: 'hour'
    }))

    const modelChart = wrapper.find('[data-test="model-chart"]')
    const groupChart = wrapper.find('[data-test="group-chart"]')

    expect(modelChart.find('.metric').text()).toBe('tokens')
    expect(groupChart.find('.metric').text()).toBe('tokens')

    await modelChart.find('.switch-metric').trigger('click')
    await flushPromises()

    expect(modelChart.find('.metric').text()).toBe('actual_cost')
    expect(groupChart.find('.metric').text()).toBe('tokens')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)

    await groupChart.find('.switch-metric').trigger('click')
    await flushPromises()

    expect(modelChart.find('.metric').text()).toBe('actual_cost')
    expect(groupChart.find('.metric').text()).toBe('actual_cost')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
  })
})

describe('admin UsageView request ID column visibility', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.mocked(localStorage.getItem).mockReset().mockReturnValue(null)
    vi.mocked(localStorage.setItem).mockReset()
    list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockReset().mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('keeps request ID hidden by default and allows enabling it from column settings', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: UsageTableStub,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          AuditLogModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: true,
          GroupDistributionChart: true,
          EndpointDistributionChart: true,
          UserTokenRanking: true,
        },
      },
    })
    await wrapper.vm.$nextTick()

    const usageTable = wrapper.findComponent(UsageTableStub)
    expect(usageTable.props('columns')).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'request_id' })]),
    )

    await wrapper.get('button[title="admin.users.columnSettings"]').trigger('click')
    const requestIdToggle = wrapper.findAll('button').find((button) => button.text() === 'Request ID')
    expect(requestIdToggle).toBeDefined()
    await requestIdToggle!.trigger('click')

    expect(usageTable.props('columns')).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'request_id', label: 'Request ID' })]),
    )
    expect(localStorage.setItem).toHaveBeenCalledWith(
      'usage-hidden-columns-version',
      'upstream-request-id-hidden-by-default',
    )
  })

  it('keeps upstream ID hidden by default and allows enabling it from column settings', async () => {
    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: UsageTableStub,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          AuditLogModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: true,
          GroupDistributionChart: true,
          EndpointDistributionChart: true,
          UserTokenRanking: true,
        },
      },
    })
    await wrapper.vm.$nextTick()

    const usageTable = wrapper.findComponent(UsageTableStub)
    expect(usageTable.props('columns')).not.toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'upstream_request_id' })]),
    )

    await wrapper.get('button[title="admin.users.columnSettings"]').trigger('click')
    const upstreamToggle = wrapper.findAll('button').find((button) => button.text() === 'Upstream ID')
    expect(upstreamToggle).toBeDefined()
    await upstreamToggle!.trigger('click')

    expect(usageTable.props('columns')).toEqual(
      expect.arrayContaining([expect.objectContaining({ key: 'upstream_request_id', label: 'Upstream ID' })]),
    )
  })
})

describe('admin UsageView handleUserClick', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getById.mockReset()

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({ trend: [], models: [], groups: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('opens user via include_deleted when clicking a usage row user', async () => {
    getById.mockResolvedValue({ id: 2, email: 'd@test.com', deleted_at: '2026-05-28T00:00:00Z' })

    const wrapper = mount(UsageView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          UsageStatsCards: true,
          UsageFilters: UsageFiltersStub,
          UsageTable: UsageTableStub,
          UsageExportProgress: true,
          UsageCleanupDialog: true,
          UserBalanceHistoryModal: true,
          AuditLogModal: true,
          Pagination: true,
          Select: true,
          DateRangePicker: true,
          Icon: true,
          TokenUsageTrend: true,
          ModelDistributionChart: true,
          GroupDistributionChart: true,
          EndpointDistributionChart: true,
          UserTokenRanking: true,
        },
      },
    })

    vi.advanceTimersByTime(120)
    await flushPromises()

    await wrapper.find('[data-test="usage-table"] .user-click').trigger('click')
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(2, true)
  })
})

describe('admin UsageView errors tab filter forwarding', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getModelStats.mockReset()
    listErrorLogs.mockReset()

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockResolvedValue({ models: [] })
    listErrorLogs.mockResolvedValue({ items: [], total: 0, pages: 0 })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('forwards model/account_id/group_id to listErrorLogs on the errors tab', async () => {
    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, AuditLogModal: true, Pagination: true, Select: true,
        DateRangePicker: true, Icon: true, TokenUsageTrend: true,
        ModelDistributionChart: true, GroupDistributionChart: true, EndpointDistributionChart: true,
        UserTokenRanking: true, OpsErrorLogTable: true, OpsErrorDetailModal: true,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()

    // 模拟用户在过滤器里选择了模型/账户/分组
    const vm = wrapper.vm as any
    vm.filters.model = 'gpt-5.3-codex'
    vm.filters.account_id = 7
    vm.filters.group_id = 3
    await flushPromises()

    // 切换到「错误请求」标签（第二个 tab 按钮）触发 loadAdminErrors
    const tabs = wrapper.findAll('[data-testid="usage-detail-tab"]')
    await tabs[1].trigger('click')
    await flushPromises()

    expect(listErrorLogs).toHaveBeenCalledWith(expect.objectContaining({
      view: 'all',
      model: 'gpt-5.3-codex',
      account_id: 7,
      group_id: 3,
    }))
  })
})

describe('admin UsageView ranking tab', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    list.mockReset()
    getStats.mockReset()
    getSnapshotV2.mockReset()
    getModelStats.mockReset()

    list.mockResolvedValue({ items: [], total: 0, pages: 0 })
    getStats.mockResolvedValue({
      total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
      total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
    })
    getSnapshotV2.mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockResolvedValue({ models: [] })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('mounts ranking lazily and drill-down sets user filter then jumps back to usage tab', async () => {
    const wrapper = mount(UsageView, {
      global: { stubs: {
        AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
        UsageTable: true, UsageExportProgress: true, UsageCleanupDialog: true,
        UserBalanceHistoryModal: true, Pagination: true, Select: true,
        DateRangePicker: true, Icon: true, TokenUsageTrend: true,
        ModelDistributionChart: true, GroupDistributionChart: true, EndpointDistributionChart: true,
        UserTokenRanking: UserTokenRankingStub, OpsErrorLogTable: true, OpsErrorDetailModal: true,
      } },
    })
    vi.advanceTimersByTime(120)
    await flushPromises()

    // 懒挂载:切到排行 tab 前不渲染
    expect(wrapper.find('[data-test="ranking"]').exists()).toBe(false)

    const tabs = wrapper.findAll('[data-testid="usage-detail-tab"]')
    expect(tabs).toHaveLength(3)
    await tabs[2].trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="ranking"]').exists()).toBe(true)

    // 下钻:设置 user_id、切回用量明细 tab 并按新筛选重新拉取列表
    list.mockClear()
    await wrapper.find('[data-test="ranking"] .pick-user').trigger('click')
    await flushPromises()

    expect((wrapper.vm as any).activeTab).toBe('usage')
    expect((wrapper.vm as any).filters.user_id).toBe(5)
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ user_id: 5 }), expect.anything())
  })
})

describe('admin UsageView model audit export', () => {
	beforeEach(() => {
		vi.useFakeTimers()
		list.mockReset().mockResolvedValue({ items: [], total: 0, pages: 0 })
		exportList.mockReset().mockResolvedValue({
			items: [{
				id: 1,
				created_at: '2026-08-04T00:00:00Z',
				model: 'gpt-5.6-sol',
				upstream_model: 'gpt-5.5',
				upstream_response_model: 'gpt-5.4',
				upstream_model_mismatch: true,
				request_type: 'sync',
				input_tokens: 1,
				output_tokens: 1,
				cache_read_tokens: 0,
				cache_creation_tokens: 0,
				duration_ms: 10,
			}],
			total: 1,
			pages: 1,
		})
		getStats.mockReset().mockResolvedValue({
			total_requests: 0, total_input_tokens: 0, total_output_tokens: 0,
			total_cache_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, average_duration_ms: 0,
		})
		getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
		getModelStats.mockReset().mockResolvedValue({ models: [] })
		saveAs.mockClear()
	})

	afterEach(() => {
		vi.useRealTimers()
	})

	it('submits an admin export task without loading history pages', async () => {
    const wrapper = mountRouteFilteredUsageView()
    vi.advanceTimersByTime(120)
    await flushPromises()
    ;(wrapper.vm as any).filters.native_compaction_v2 = true
    exportList.mockClear()
    await (wrapper.vm as any).exportToExcel()
    expect(submitExport).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }))
    expect(exportList).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('admin UsageView live log', () => {
  let atTop = true
  const LiveUsageTableStub = {
    name: 'UsageTable',
    props: ['data', 'loading', 'rowClass'],
    methods: { isAtTop: () => atTop, scrollToTop: () => undefined },
    // The "N new records" anchor sits in the table's above-table slot (7j-4).
    template: '<div><slot name="above-table" /></div>',
  }
  const page = (...keys: string[]) => ({
    items: keys.map((key) => {
      const [source, id] = key.split(':')
      return { id: Number(id), source: source === 'usage' ? undefined : source, user_id: 1, model: 'm', created_at: '' }
    }),
    total: keys.length,
    pages: 1,
  })

  beforeEach(() => {
    vi.useFakeTimers()
    atTop = true
    Object.keys(routeQuery).forEach((key) => delete routeQuery[key])
    getStats.mockReset().mockResolvedValue({ total_requests: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0 })
    getSnapshotV2.mockReset().mockResolvedValue({ trend: [], models: [], groups: [] })
    getModelStats.mockReset().mockResolvedValue({ models: [] })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  const mountLive = () => mount(UsageView, {
    global: { stubs: {
      AppLayout: AppLayoutStub, UsageStatsCards: true, UsageFilters: UsageFiltersStub,
      UsageTable: LiveUsageTableStub, UsageExportProgress: true, UsageCleanupDialog: true,
      UserBalanceHistoryModal: true, Pagination: true, Select: true,
      DateRangePicker: true, Icon: true, TokenUsageTrend: true,
      ModelDistributionChart: true, GroupDistributionChart: true,
      EndpointDistributionChart: true, UserTokenRanking: true,
    } },
  })
  const shownIn = (wrapper: ReturnType<typeof mountLive>) => () =>
    (wrapper.findComponent(LiveUsageTableStub).props('data') as Array<{ id: number; source?: string }>)
      .map((row) => `${row.source ?? 'usage'}:${row.id}`)

  it('follows new records at the top on page 1, newest first, with the list top in view; otherwise only counts them', async () => {
    list.mockReset().mockResolvedValue(page('usage:2', 'unbilled:2', 'usage:1'))
    const wrapper = mountLive()
    await flushPromises()
    const shown = shownIn(wrapper)
    expect(shown()).toEqual(['usage:2', 'unbilled:2', 'usage:1']) // the same id from two sources: two rows

    list.mockResolvedValue(page('usage:3', 'usage:2', 'unbilled:2', 'usage:1'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(shown()).toEqual(['usage:3', 'usage:2', 'unbilled:2', 'usage:1'])

    atTop = false // the user scrolled into the list
    list.mockResolvedValue(page('usage:4', 'usage:3', 'usage:2', 'unbilled:2', 'usage:1'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(shown()).toEqual(['usage:3', 'usage:2', 'unbilled:2', 'usage:1'])
    expect((wrapper.vm as any).liveUsage.pending.value).toBe(1)

    atTop = true
    ;(wrapper.vm as any).handleSort('model', 'asc') // not newest first: no following
    await flushPromises()
    list.mockResolvedValue(page('usage:5', 'usage:4', 'usage:3', 'usage:2', 'unbilled:2', 'usage:1'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(shown()).toEqual(['usage:4', 'usage:3', 'usage:2', 'unbilled:2', 'usage:1'])
    expect((wrapper.vm as any).liveUsage.pending.value).toBe(1)
    wrapper.unmount()
  })

  it('holds the list\'s 5 s check on the errors and ranking tabs while the cards go on, and checks at once on the way back (8.5a)', async () => {
    list.mockReset().mockResolvedValue(page('usage:1'))
    listErrorLogs.mockReset().mockResolvedValue({ items: [], total: 0 })
    const wrapper = mountLive()
    await flushPromises()
    const tabs = wrapper.findAll('[data-testid="usage-detail-tab"]')
    const listed = list.mock.calls.length
    const stats = getStats.mock.calls.length

    await tabs[1].trigger('click') // errors
    await vi.advanceTimersByTimeAsync(30_000)
    expect(list.mock.calls.length).toBe(listed)
    expect(getStats.mock.calls.length).toBe(stats + 1)

    await tabs[0].trigger('click')
    await vi.advanceTimersByTimeAsync(0)
    expect(list.mock.calls.length).toBe(listed + 1)
    wrapper.unmount()
  })

  it('offers the records that came in while away from the top as "N new records"; a click takes them in', async () => {
    list.mockReset().mockResolvedValue(page('usage:1'))
    const wrapper = mountLive()
    await flushPromises()
    const shown = shownIn(wrapper)
    expect(wrapper.find('[data-test="live-status"]').exists()).toBe(true)

    atTop = false
    list.mockResolvedValue(page('usage:2', 'usage:1'))
    await vi.advanceTimersByTimeAsync(5000)
    const notice = wrapper.get('[data-test="pending-changes"]')
    expect(notice.text()).toContain('admin.usage.newRecords')
    expect(shown()).toEqual(['usage:1'])

    await notice.trigger('click')
    await flushPromises()
    expect(shown()).toEqual(['usage:2', 'usage:1'])
    wrapper.unmount()
  })
})
