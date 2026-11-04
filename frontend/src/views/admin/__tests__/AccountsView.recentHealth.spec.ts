import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, getBatchRecentHealth } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  getBatchRecentHealth: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groupQualityGate: {
      getStatus: vi.fn().mockResolvedValue({ generated_at: '', stale: false, groups: [] }),
      getPolicies: vi.fn().mockResolvedValue({ version: 0, groups: {} }),
    },
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
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

// Renders only the status cell of each row.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id">
        <div :data-test="'status-' + row.id"><slot name="cell-status" :row="row" /></div>
        <div :data-test="'recent-' + row.id"><slot name="cell-recent_health" :row="row" /></div>
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

describe('admin AccountsView recent health column', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({
      items: [
        { id: 1, name: 'busy', platform: 'openai', type: 'apikey', status: 'active', schedulable: true },
        { id: 2, name: 'quiet', platform: 'openai', type: 'apikey', status: 'active', schedulable: true },
        { id: 3, name: 'implicit', platform: 'gemini', type: 'apikey', status: 'active', schedulable: true }
      ],
      total: 3,
      page: 1,
      page_size: 20,
      pages: 1
    })
    const cache = { samples: 50, kept: 30, zero: 10, partial: 10, mid: 5, full: 25, read_tokens: 500, prompt_tokens: 1000, span_seconds: 1800 }
    getBatchRecentHealth.mockReset().mockResolvedValue({
      stats: {
        '1': {
          failure: { calls: 200, failures: 30, wilson_low: 0.107, wilson_high: 0.205, first_output_timeouts: 10, stream_failures: 15, other_failures: 5, span_seconds: 1500 },
          first_output: { samples: 50, p50_ms: 4000, p90_ms: 12_000, slow_share: 0.12, span_seconds: 3000 },
          cache
        },
        '3': { failure: null, first_output: null, cache }
      },
      failure_max_events: 200,
      failure_max_age_seconds: 3600,
      sample_max: 50,
      sample_max_age_seconds: 7200,
      min_display_samples: 20,
      min_color_samples: 50
    })
  })

  it('asks for the visible accounts and shows labelled errors and first token in their own column', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getBatchRecentHealth).toHaveBeenCalledWith([1, 2, 3])

    const status = wrapper.get('[data-test="status-1"]')
    expect(status.get('[data-test="status-badge"]').text()).toBe('active')
    expect(status.find('[data-test="recent-failures"]').exists()).toBe(false)
    const busy = wrapper.get('[data-test="recent-1"]')
    const failures = busy.get('[data-test="recent-failures"]')
    expect(failures.text()).toBe('admin.accounts.recentFailureRate:{"failures":30,"calls":200,"rate":"15.0%"}')
    expect(failures.attributes('href')).toBe('/admin/usage?tab=errors&account_id=1')
    expect(failures.attributes('target')).toBe('_blank')
    expect(busy.get('[data-test="recent-first-output"]').text()).toBe('admin.accounts.recentFirstOutputP90:{"value":"12.0s"}')

    const quiet = wrapper.get('[data-test="recent-2"]')
    expect(quiet.find('[data-test="recent-failures"]').exists()).toBe(false)
    expect(quiet.find('[data-test="recent-first-output"]').exists()).toBe(false)
    expect(quiet.get('[data-test="recent-empty"]').text()).toBe('-')
    expect(quiet.get('[data-test="recent-empty"]').attributes('title')).toBe('admin.accounts.recentHealthEmptyTitle')
  })

  it('only explains an empty dash after the batch loaded, and says so while loading or after a failure', async () => {
    let finish!: (value: unknown) => void
    getBatchRecentHealth.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    const wrapper = mountView()
    await flushPromises()
    const dashTitle = () => wrapper.get('[data-test="recent-2"] [data-test="recent-empty"]').attributes('title')
    expect(dashTitle()).toBe('admin.accounts.recentHealthLoadingTitle')

    finish({ stats: {} })
    await flushPromises()
    expect(dashTitle()).toBe('admin.accounts.recentHealthEmptyTitle')

    getBatchRecentHealth.mockRejectedValueOnce(new Error('boom'))
    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 1)
    await flushPromises()
    expect(dashTitle()).toBe('admin.accounts.recentHealthFailedTitle')
  })

  it('passes the row platform so a Gemini cache line stays uncoloured', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="recent-1"] [data-test="recent-cache"]').classes()).toContain('text-red-600')
    expect(wrapper.get('[data-test="recent-3"] [data-test="recent-cache"]').classes()).toContain('text-gray-500')
  })

  describe('after the table loader reloads rows', () => {
    const row = (id: number, name: string) => ({ id, name, platform: 'openai', type: 'apikey', status: 'active', schedulable: true })

    beforeEach(() => {
      listAccounts
        .mockResolvedValueOnce({ items: [row(1, 'busy'), row(2, 'quiet')], total: 3, page: 1, page_size: 2, pages: 2 })
        .mockResolvedValue({ items: [row(3, 'next')], total: 3, page: 2, page_size: 2, pages: 2 })
    })

    afterEach(() => {
      vi.useRealTimers()
    })

    it('asks once on the initial load and again for the new rows after a page change', async () => {
      const wrapper = mountView()
      await flushPromises()
      expect(getBatchRecentHealth).toHaveBeenCalledTimes(1)

      wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
      await flushPromises()

      expect(listAccounts).toHaveBeenLastCalledWith(2, expect.any(Number), expect.anything(), expect.anything())
      expect(getBatchRecentHealth).toHaveBeenCalledTimes(2)
      expect(getBatchRecentHealth).toHaveBeenLastCalledWith([3])
    })

    it('asks again after a page-size change', async () => {
      const wrapper = mountView()
      await flushPromises()

      wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:pageSize', 50)
      await flushPromises()

      expect(listAccounts).toHaveBeenLastCalledWith(1, 50, expect.anything(), expect.anything())
      expect(getBatchRecentHealth).toHaveBeenCalledTimes(2)
      expect(getBatchRecentHealth).toHaveBeenLastCalledWith([3])
    })

    it('asks again after the debounced filter reload', async () => {
      vi.useFakeTimers()
      const wrapper = mountView()
      await flushPromises()

      wrapper.getComponent({ name: 'AccountTableFilters' }).vm.$emit('change')
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()

      expect(listAccounts).toHaveBeenCalledTimes(2)
      expect(getBatchRecentHealth).toHaveBeenCalledTimes(2)
      expect(getBatchRecentHealth).toHaveBeenLastCalledWith([3])
    })
  })

  it('skips the request while the recent health column is hidden', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['recent_health']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    mountView()
    await flushPromises()
    expect(getBatchRecentHealth).not.toHaveBeenCalled()
  })
})
