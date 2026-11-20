import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  getAllGroups,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  listAccounts,
  showError,
  showSuccess,
  isCurrentStep,
  nextStep,
  authState,
  listGroupCategories,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getAllGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  listAccounts: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  isCurrentStep: vi.fn(),
  nextStep: vi.fn(),
  authState: { isSimpleMode: false },
  listGroupCategories: vi.fn(),
}))

const messages: Record<string, string> = {
  'admin.groups.columnSettings': 'Column Settings',
  'admin.groups.columns.name': 'Name',
  'admin.groups.columns.id': 'ID',
  'admin.groups.columns.platform': 'Platform',
  'admin.groups.columns.billingType': 'Billing Type',
  'admin.groups.columns.rateMultiplier': 'Rate Multiplier',
  'admin.groups.columns.type': 'Type',
  'admin.groups.columns.accounts': 'Accounts',
  'admin.groups.columns.capacity': 'Capacity',
  'admin.groups.columns.usage': 'Usage',
  'admin.groups.columns.status': 'Status',
  'admin.groups.columns.actions': 'Actions',
  'admin.groups.usageToday': 'Today',
  'admin.groups.usageYesterday': 'Yesterday',
  'admin.groups.usageTotal': 'Total',
}

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: getAllGroups,
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: listAccounts,
    },
  },
}))

vi.mock('@/api/admin/groupCategories', () => ({
  default: { list: listGroupCategories, save: vi.fn(), setGroupCategories: vi.fn() },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState,
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep,
    nextStep,
  }),
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

const createGroup = (overrides: Partial<AdminGroup> = {}): AdminGroup => ({
  id: 1,
  name: 'Core Anthropic',
  description: null,
  platform: 'anthropic',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  default_mapped_model: '',
  messages_dispatch_model_config: undefined,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 3,
  active_account_count: 2,
  rate_limited_account_count: 1,
  model_allowlist: undefined,
  sort_order: 10,
  ...overrides,
})

const AppLayoutStub = {
  template: '<div><slot /></div>',
}

const TablePageLayoutStub = {
  template: `
    <div>
      <slot name="filters" />
      <slot name="table" />
      <slot name="pagination" />
    </div>
  `,
}

const DataTableStub = {
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div>
      <div data-test="columns">{{ columns.map((col) => col.key).join(',') }}</div>
      <div data-test="rows">{{ data.map((row) => row.name).join(',') }}</div>
      <div v-if="data.length" data-test="usage-cell">
        <slot name="cell-usage" :row="data[0]" />
      </div>
      <div v-if="data.length" data-test="capacity-cell">
        <slot name="cell-capacity" :row="data[0]" />
      </div>
      <div v-if="data.length" data-test="billing-cell">
        <slot name="cell-billing_type" :row="data[0]" />
      </div>
      <div v-if="data.length" data-test="rate-cell">
        <slot name="cell-rate_multiplier" :row="data[0]" :value="data[0].rate_multiplier" />
      </div>
      <div v-if="data.length" data-test="accounts-cell">
        <slot name="cell-account_count" :row="data[0]" />
      </div>
    </div>
  `,
}

const SelectStub = {
  props: ['modelValue', 'options', 'placeholder'],
  emits: ['update:modelValue', 'change'],
  template: `
    <select
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value); $emit('change')"
    >
      <option v-for="option in options" :key="String(option.value)" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `,
}

const BaseDialogStub = {
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
}

const IconStub = {
  props: ['name'],
  template: '<span data-test="icon">{{ name }}</span>',
}

const mountView = async () => {
  const wrapper = mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        PlatformIcon: true,
        Icon: IconStub,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: { template: '<div><slot /></div>' },
      },
    },
  })
  await flushPromises()
  return wrapper
}

const columnKeys = (wrapper: ReturnType<typeof mount>) =>
  wrapper.get('[data-test="columns"]').text().split(',').filter(Boolean)

const openColumnSettings = async (wrapper: ReturnType<typeof mount>) => {
  await wrapper.get('button[aria-label="Column Settings"]').trigger('click')
}

const clickColumnToggle = async (wrapper: ReturnType<typeof mount>, label: string) => {
  const button = wrapper
    .findAll('button')
    .find((item) => item.text().includes(label))
  expect(button, `column toggle ${label}`).toBeTruthy()
  await button!.trigger('click')
  await flushPromises()
}

describe('admin GroupsView column settings', () => {
  beforeEach(() => {
    localStorage.clear()

    listGroups.mockReset()
    getAllGroups.mockReset()
    getModelAllowlistCandidates.mockReset()
    getUsageSummary.mockReset()
    getCapacitySummary.mockReset()
    getLiveCapability.mockReset()
    listAccounts.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    isCurrentStep.mockReset()
    nextStep.mockReset()
    authState.isSimpleMode = false
    listGroupCategories.mockReset()
    listGroupCategories.mockResolvedValue([])

    listGroups.mockResolvedValue({
      items: [createGroup()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    getAllGroups.mockResolvedValue([])
    getModelAllowlistCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false })
    listAccounts.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    isCurrentStep.mockReturnValue(false)
  })

  it('does not call advanced group APIs or expose the exclusive filter in simple mode', async () => {
    authState.isSimpleMode = true
    const wrapper = await mountView()

    expect(getLiveCapability).not.toHaveBeenCalled()
    expect(getModelAllowlistCandidates).not.toHaveBeenCalled()
    expect(getUsageSummary).not.toHaveBeenCalled()
    expect(getCapacitySummary).not.toHaveBeenCalled()
    expect(listGroupCategories).not.toHaveBeenCalled()
    expect(listGroups).toHaveBeenCalledWith(
      expect.any(Number),
      expect.any(Number),
      expect.objectContaining({ is_exclusive: undefined }),
      expect.anything(),
    )
    expect(wrapper.find('select').text()).not.toContain('admin.groups.allGroups')
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('hides the id column by default while keeping other group columns visible', async () => {
    const wrapper = await mountView()

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'category',
      'sched_gate',
      'account_count',
      'capacity',
      'usage',
      'status',
      'actions',
    ])
    // The defaults apply without being written (two column memories, mobile list plan); the version is current.
    expect(localStorage.getItem('group-hidden-columns')).toBeNull()
    expect(localStorage.getItem('group-column-settings-version')).toBe('2')
  })

  it('applies saved hidden columns on mount and ignores unknown keys', async () => {
    localStorage.setItem(
      'group-hidden-columns',
      JSON.stringify(['usage', 'capacity', 'removed_column', 'name', 'actions']),
    )
    localStorage.setItem('group-column-settings-version', '2')

    const wrapper = await mountView()

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'id',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'category',
      'sched_gate',
      'account_count',
      'status',
      'actions',
    ])
  })

  it('auto-hides id for existing saved column prefs after version bump', async () => {
    localStorage.setItem('group-hidden-columns', JSON.stringify(['usage']))
    // No version key → treated as version 1, migrate to 2 and hide id.

    const wrapper = await mountView()

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'category',
      'sched_gate',
      'account_count',
      'capacity',
      'status',
      'actions',
    ])
    expect(JSON.parse(localStorage.getItem('group-hidden-columns')!)).toEqual(
      expect.arrayContaining(['usage', 'id']),
    )
    expect(localStorage.getItem('group-column-settings-version')).toBe('2')
  })

  it('toggles a column and persists hidden column keys', async () => {
    const wrapper = await mountView()

    await openColumnSettings(wrapper)
    await clickColumnToggle(wrapper, 'Usage')

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'category',
      'sched_gate',
      'account_count',
      'capacity',
      'status',
      'actions',
    ])
    expect(JSON.parse(localStorage.getItem('group-hidden-columns')!)).toEqual(
      expect.arrayContaining(['id', 'usage']),
    )
  })

  it('can show the id column from column settings', async () => {
    const wrapper = await mountView()

    await openColumnSettings(wrapper)
    await clickColumnToggle(wrapper, 'ID')

    expect(columnKeys(wrapper)).toEqual([
      'name',
      'id',
      'platform',
      'billing_type',
      'rate_multiplier',
      'is_exclusive',
      'category',
      'sched_gate',
      'account_count',
      'capacity',
      'usage',
      'status',
      'actions',
    ])
    expect(localStorage.getItem('group-hidden-columns')).toBe(JSON.stringify([]))
  })

  it('skips usage and capacity fetches until consuming columns are shown', async () => {
    localStorage.setItem(
      'group-hidden-columns',
      JSON.stringify(['billing_type', 'usage', 'capacity']),
    )

    const wrapper = await mountView()

    expect(getUsageSummary).not.toHaveBeenCalled()
    expect(getCapacitySummary).not.toHaveBeenCalled()

    await openColumnSettings(wrapper)
    await clickColumnToggle(wrapper, 'Usage')
    expect(getUsageSummary).toHaveBeenCalledTimes(1)
    expect(getUsageSummary).toHaveBeenCalledWith()
    expect(getCapacitySummary).not.toHaveBeenCalled()

    await clickColumnToggle(wrapper, 'Capacity')
    expect(getUsageSummary).toHaveBeenCalledTimes(1)
    expect(getCapacitySummary).toHaveBeenCalledTimes(1)
  })

  it('renders yesterday usage between today and total', async () => {
    getUsageSummary.mockResolvedValue([
      { group_id: 1, today_cost: 1.25, yesterday_cost: 2.5, total_cost: 9.75 },
    ])

    const wrapper = await mountView()
    const text = wrapper.get('[data-test="usage-cell"]').text()

    expect(text).toContain('Today$1.25')
    expect(text).toContain('Yesterday$2.50')
    expect(text).toContain('Total$9.75')
    expect(text.indexOf('Today')).toBeLessThan(text.indexOf('Yesterday'))
    expect(text.indexOf('Yesterday')).toBeLessThan(text.indexOf('Total'))
  })

  it('writes large amounts in three significant digits, never cut, the exact one in the tip (7o-4)', async () => {
    getUsageSummary.mockResolvedValue([
      { group_id: 1, today_cost: 30135.12, yesterday_cost: 2.5, total_cost: 1234567 },
    ])

    const wrapper = await mountView()
    const text = wrapper.get('[data-test="usage-cell"]').text()
    expect(text).toContain('Today$30.1K')
    expect(text).toContain('Total$1.23M')

    document.body.appendChild(wrapper.element)
    vi.useFakeTimers()
    const today = wrapper.findAll('[data-test="usage-cell"] span').find((span) => span.text() === '$30.1K')!
    today.element.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    expect(document.getElementById('sq-tip')!.textContent).toBe('$30,135.12')
    today.element.dispatchEvent(new Event('mouseleave'))
    vi.useRealTimers()
    wrapper.unmount()
  })

  it('writes subscription limits one to a line in three significant digits, the exact one in the tip (7r-2)', async () => {
    listGroups.mockResolvedValue({
      items: [createGroup({ subscription_type: 'subscription', daily_limit_usd: 100, weekly_limit_usd: 500, monthly_limit_usd: 2000 })],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    getUsageSummary.mockResolvedValue([
      { group_id: 1, today_cost: 12.5, yesterday_cost: 0, total_cost: 1234567 },
    ])

    const wrapper = await mountView()
    const lines = wrapper.findAll('[data-test="group-limits"] > div').map((line) => line.text().replace(/\s+/g, ''))
    expect(lines).toEqual([
      '$12.50/$100.00/admin.groups.limitDay',
      '$500.00/admin.groups.limitWeek',
      '$2.00K/admin.groups.limitMonth',
      'Total$1.23M',
    ])

    document.body.appendChild(wrapper.element)
    vi.useFakeTimers()
    const monthly = wrapper.findAll('[data-test="billing-cell"] span').find((span) => span.text() === '$2.00K')!
    monthly.element.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    expect(document.getElementById('sq-tip')!.textContent).toBe('$2,000.00')
    monthly.element.dispatchEvent(new Event('mouseleave'))
    vi.useRealTimers()
    wrapper.unmount()
  })

  it('passes the group concurrency from the capacity summary to the badge', async () => {
    getCapacitySummary.mockResolvedValue([
      { group_id: 1, concurrency_used: 5, concurrency_max: 600, concurrency_group_used: 3, sessions_used: 0, sessions_max: 0, rpm_used: 0, rpm_max: 0 },
    ])

    const wrapper = await mountView()
    const badge = wrapper.getComponent({ name: 'GroupCapacityBadge' })

    expect(badge.props()).toMatchObject({ concurrencyUsed: 5, concurrencyMax: 600, concurrencyGroupUsed: 3 })
  })

  // Mobile list plan, step 2: below 768px the phone's own memory and defaults (name, platform, accounts, status, then
  // "⋯"); a change there is written to the phone's key only, and "restore defaults" removes that key alone.
  describe('below 768px', () => {
    const setPhone = () => vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
      matches: query.includes('min-width') ? false : true, media: query, onchange: null,
      addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
    }) as unknown as MediaQueryList)
    afterEach(() => { vi.restoreAllMocks() })

    it('shows the phone defaults and keeps a change in the phone memory only', async () => {
      setPhone()
      localStorage.setItem('group-hidden-columns', JSON.stringify(['usage']))
      localStorage.setItem('group-column-settings-version', '2')
      const wrapper = await mountView()
      expect(columnKeys(wrapper)).toEqual(['name', 'platform', 'account_count', 'status', 'actions'])
      await openColumnSettings(wrapper)
      await clickColumnToggle(wrapper, 'Usage')
      expect(columnKeys(wrapper)).toContain('usage')
      expect(JSON.parse(localStorage.getItem('group-hidden-columns-narrow')!)).not.toContain('usage')
      expect(localStorage.getItem('group-hidden-columns')).toBe(JSON.stringify(['usage']))
      await wrapper.get('[data-test="group-columns-reset"]').trigger('click')
      await flushPromises()
      expect(columnKeys(wrapper)).toEqual(['name', 'platform', 'account_count', 'status', 'actions'])
      expect(localStorage.getItem('group-hidden-columns-narrow')).toBeNull()
    })

    it('shows the phone toolbar: count, filters as removable tags, columns in a sheet', async () => {
      setPhone()
      const wrapper = await mountView()
      expect(wrapper.find('[data-test="groups-count"]').exists()).toBe(true)
      expect(wrapper.find('[data-test="table-toolbar-phone"]').exists()).toBe(true)
      ;(wrapper.vm as any).filters.platform = 'openai'
      await flushPromises()
      const tag = wrapper.get('[data-testid="usage-filter-tag"]')
      expect(tag.text()).toContain('Platform')
      listGroups.mockClear()
      await tag.get('button').trigger('click')
      await flushPromises()
      expect(listGroups).toHaveBeenCalled()
      expect(listGroups.mock.calls.at(-1)![2]).toMatchObject({ platform: undefined })
      expect(wrapper.find('[data-testid="usage-filter-tag"]').exists()).toBe(false)
      await wrapper.get('[data-test="group-columns"]').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-test="group-column-settings"]').exists()).toBe(true)
    })
  })

  // Mobile list plan, copy: the rate as "1.0x"; the accounts as "available / total", rate-limited ones after it.
  it('writes the rate as 1.5x and the accounts as available / total', async () => {
    listGroups.mockResolvedValue({
      items: [{ id: 1, name: 'g', platform: 'anthropic', rate_multiplier: 1.5, status: 'active', active_account_count: 2, account_count: 3, rate_limited_account_count: 1, subscription_type: 'standard', is_exclusive: false, sort_order: 0 }],
      total: 1, page: 1, page_size: 20, pages: 1,
    })
    const wrapper = await mountView()
    expect(wrapper.get('[data-test="rate-cell"]').text()).toBe('1.5x')
    const accounts = wrapper.get('[data-test="group-account-count"]')
    expect(accounts.findAll(':scope > span').map((el) => el.text())).toEqual(['2', '/', '3', 'admin.groups.accountsLimitedShort'])
  })
})
