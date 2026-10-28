import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

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

const AccountGroupsCellButtonStub = {
  name: 'AccountGroupsCellButton',
  emits: ['open'],
  template: '<button type="button" />'
}

// 渲染 #after 插槽，好让测试打开「更多操作」下拉里的列设置。
const AccountTableActionsStub = {
  name: 'AccountTableActions',
  emits: ['refresh', 'create'],
  template: '<div><slot name="after" /></div>'
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
        AccountGroupsCellButton: AccountGroupsCellButtonStub,
        AccountGroupsDialog: AccountGroupsDialogStub,
        AccountTableActions: AccountTableActionsStub,
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

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const balanceText = (wrapper: ReturnType<typeof mountView>, id: number) =>
  wrapper.get(`[data-test="balance-${id}"] [data-testid="upstream-balance-value"]`).text()

// 卸载每个用例挂载的页面，避免它们的轮询和 visibilitychange 监听串到后面的用例。
enableAutoUnmount(afterEach)

describe('admin AccountsView upstream balance column', () => {
  let hidden: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    localStorage.clear()
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
    hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    vi.spyOn(console, 'error').mockImplementation(() => {})
    listAccounts.mockReset().mockResolvedValue(page([1, 2]))
    getUpstreamBalances.mockReset().mockImplementation(async (ids: number[]) =>
      answer(...ids.map(id => okItem(id, id === 1 ? 12.34 : 7)))
    )
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('asks for the visible accounts on load and every 30 seconds', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
    expect(getUpstreamBalances).toHaveBeenCalledWith([1, 2], expect.objectContaining({ signal: expect.anything() }))
    expect(balanceText(wrapper, 1)).toBe('$12.34')
    expect(balanceText(wrapper, 2)).toBe('$7.00')

    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
    expect(getUpstreamBalances).toHaveBeenLastCalledWith([1, 2], expect.objectContaining({ signal: expect.anything() }))
  })

  it('asks again right after a page change with only the new page ids', async () => {
    listAccounts.mockReset().mockResolvedValueOnce(page([1, 2], 1, 3)).mockResolvedValue(page([3], 2, 3))
    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()

    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
    expect(getUpstreamBalances).toHaveBeenLastCalledWith([3], expect.anything())
  })

  it('skips polling while a dialog is open', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)

    wrapper.getComponent({ name: 'AccountTableActions' }).vm.$emit('create')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30_000)

    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('asks again as soon as a dialog closes', async () => {
    const wrapper = mountView()
    await flushPromises()

    wrapper.findAllComponents({ name: 'AccountGroupsCellButton' })[0].vm.$emit('open')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)

    wrapper.getComponent({ name: 'AccountGroupsDialog' }).vm.$emit('close')
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
  })

  it('skips polling while the browser tab is hidden and asks again when it is visible', async () => {
    mountView()
    await flushPromises()

    hidden.mockReturnValue(true)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)

    hidden.mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
  })

  it('does not ask while the upstream balance column is hidden', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['upstream_balance']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(30_000)

    expect(getUpstreamBalances).not.toHaveBeenCalled()
  })

  it('does not ask again when the same accounts only change order', async () => {
    listAccounts.mockReset().mockResolvedValueOnce(page([1, 2])).mockResolvedValue(page([2, 1]))
    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent({ name: 'AccountTableFilters' }).vm.$emit('change')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('ignores a late answer for the previous page and aborts it', async () => {
    listAccounts.mockReset().mockResolvedValueOnce(page([1, 2], 1, 3)).mockResolvedValue(page([3], 2, 3))
    let resolveFirst!: (value: unknown) => void
    getUpstreamBalances
      .mockReset()
      .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
      .mockResolvedValue(answer(okItem(3, 3.5)))
    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent({ name: 'Pagination' }).vm.$emit('update:page', 2)
    await flushPromises()
    expect(getUpstreamBalances.mock.calls[0][1].signal.aborted).toBe(true)
    expect(balanceText(wrapper, 3)).toBe('$3.50')

    resolveFirst(answer(okItem(1, 1), okItem(3, 99)))
    await flushPromises()
    expect(balanceText(wrapper, 3)).toBe('$3.50')
  })

  it('asks in chunks of at most 200 ids', async () => {
    const ids = Array.from({ length: 450 }, (_, i) => i + 1)
    listAccounts.mockReset().mockResolvedValue(page(ids))
    mountView()
    await flushPromises()

    expect(getUpstreamBalances).toHaveBeenCalledTimes(3)
    expect(getUpstreamBalances.mock.calls[0][0]).toEqual(ids.slice(0, 200))
    expect(getUpstreamBalances.mock.calls[1][0]).toEqual(ids.slice(200, 400))
    expect(getUpstreamBalances.mock.calls[2][0]).toEqual(ids.slice(400))
  })

  it('keeps the previous values of a chunk that failed', async () => {
    const ids = Array.from({ length: 250 }, (_, i) => i + 1)
    listAccounts.mockReset().mockResolvedValue(page(ids))
    const wrapper = mountView()
    await flushPromises()
    expect(balanceText(wrapper, 1)).toBe('$12.34')
    expect(balanceText(wrapper, 250)).toBe('$7.00')

    getUpstreamBalances.mockImplementation(async (chunk: number[]) => {
      if (chunk.includes(1)) throw new Error('boom')
      return answer(...chunk.map(id => okItem(id, 8)))
    })
    await vi.advanceTimersByTimeAsync(30_000)

    expect(balanceText(wrapper, 1)).toBe('$12.34')
    expect(balanceText(wrapper, 250)).toBe('$8.00')
  })

  it('corrects the clock with server_time before greying out a value', async () => {
    // 服务端时间比本机慢 5 分钟：observed_at = server_time - 60s，之后还有一次失败尝试。
    // 不校正时按本机时间算已过 6 分钟、会变灰；校正后只过了 1 分钟。
    getUpstreamBalances.mockReset().mockImplementation(async () => {
      const serverNow = Date.now() - 5 * 60_000
      return {
        items: [
          {
            ...okItem(1, 12.34, iso(serverNow - 60_000)),
            last_attempt_at: iso(serverNow - 1_000),
            reason: 'upstream_error'
          }
        ],
        server_time: iso(serverNow)
      }
    })
    const wrapper = mountView()
    await flushPromises()

    const value = wrapper.get('[data-test="balance-1"] [data-testid="upstream-balance-value"]')
    expect(value.text()).toBe('$12.34')
    expect(value.classes()).toContain('text-gray-800')
    expect(wrapper.get('[data-test="balance-1"] [data-testid="upstream-balance-updated"]').text()).toBe(
      'admin.accounts.upstreamBalance.updatedMinutesAgo:1'
    )
  })

  it('takes the clock offset from each chunk when its answer arrives', async () => {
    // 第一个分片慢 20 秒返回，第二个立即返回；服务端时间比本机慢 5 分钟。
    // 偏差若在全部返回后才用第二个分片的 server_time 计算，会多算 20 秒，第 1 行就显示成 40 秒前。
    const ids = Array.from({ length: 250 }, (_, i) => i + 1)
    listAccounts.mockReset().mockResolvedValue(page(ids))
    getUpstreamBalances.mockReset().mockImplementation(async (chunk: number[]) => {
      if (chunk[0] === 1) await new Promise(resolve => setTimeout(resolve, 20_000))
      const serverNow = Date.now() - 5 * 60_000
      return {
        items: chunk.map(id => ({ ...okItem(id, 1, iso(serverNow - 60_000)), last_attempt_at: iso(serverNow) })),
        server_time: iso(serverNow)
      }
    })
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(20_000)

    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
    expect(balanceText(wrapper, 1)).toBe('$1.00')
    expect(wrapper.get('[data-test="balance-1"] [data-testid="upstream-balance-updated"]').text()).toBe(
      'admin.accounts.upstreamBalance.updatedMinutesAgo:1'
    )
  })

  it('drops the cached value of an edited account and never shows an answer from before the edit', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(balanceText(wrapper, 1)).toBe('$12.34')

    // 请求 1（30 秒轮询）在途。
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    getUpstreamBalances.mockImplementationOnce(() => first.promise).mockImplementationOnce(() => second.promise)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
    const firstSignal = getUpstreamBalances.mock.calls[1][1].signal as AbortSignal

    // 账号被修改：作废请求 1、丢掉该账号的缓存值，并立即发出请求 2。
    wrapper.findAllComponents({ name: 'AccountPriorityCell' })[0].vm.$emit('updated', { ...row(1), priority: 5 })
    await flushPromises()
    expect(firstSignal.aborted).toBe(true)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(3)
    expect(balanceText(wrapper, 1)).toBe('—')

    // 请求 1 迟到的旧值被忽略；请求 2 失败也不会把旧值带回来。
    first.resolve(answer(okItem(1, 55), okItem(2, 66)))
    await flushPromises()
    second.reject(new Error('boom'))
    await flushPromises()

    expect(balanceText(wrapper, 1)).toBe('—')
    expect(balanceText(wrapper, 2)).toBe('$7.00')
  })

  it('drops cached values after a filtered bulk edit even when the refetch fails', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(balanceText(wrapper, 1)).toBe('$12.34')

    wrapper.getComponent({ name: 'AccountBulkActionsBar' }).vm.$emit('edit-filtered')
    await flushPromises()
    getUpstreamBalances.mockRejectedValue(new Error('boom'))
    // 按筛选批量编辑：所选列表为空，但当前页账号都可能被改了。
    wrapper.getComponent({ name: 'BulkEditAccountModal' }).vm.$emit('updated')
    await flushPromises()

    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
    expect(balanceText(wrapper, 1)).toBe('—')
    expect(balanceText(wrapper, 2)).toBe('—')
  })

  it('refetches when a row on the page was changed elsewhere and does not keep its old value', async () => {
    listAccounts.mockReset().mockResolvedValue(page([1, 2]))
    const wrapper = mountView()
    await flushPromises()
    expect(balanceText(wrapper, 1)).toBe('$12.34')

    // 请求 1（30 秒轮询）在途。
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    getUpstreamBalances.mockImplementationOnce(() => first.promise).mockImplementationOnce(() => second.promise)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(2)
    const firstSignal = getUpstreamBalances.mock.calls[1][1].signal as AbortSignal

    // 重载拿到同一批 id，但账号 1 的 updated_at 变了（例如别的管理员换了 Key）。
    listAccounts.mockResolvedValue(page([1, 2], 1, 2, id => row(id, id === 1 ? '2026-10-27T12:02:00Z' : undefined)))
    wrapper.getComponent({ name: 'AccountTableFilters' }).vm.$emit('change')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(firstSignal.aborted).toBe(true)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(3)

    first.resolve(answer(okItem(1, 55), okItem(2, 66)))
    await flushPromises()
    second.reject(new Error('boom'))
    await flushPromises()

    expect(balanceText(wrapper, 1)).toBe('—')
    expect(balanceText(wrapper, 2)).toBe('$7.00')
  })

  it('does not ask again when only runtime fields of a row change', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)

    listAccounts.mockResolvedValue(page([1, 2], 1, 2, id => row(id, undefined, { current_concurrency: 3, active_sessions: 2 })))
    wrapper.getComponent({ name: 'AccountTableFilters' }).vm.$emit('change')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledTimes(2)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('asks right away when the column is shown again from the open tools menu', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['upstream_balance']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    const wrapper = mountView()
    await flushPromises()
    expect(getUpstreamBalances).not.toHaveBeenCalled()

    await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
    wrapper.getComponent({ name: 'AccountColumnSettings' }).vm.$emit('toggle', 'upstream_balance')
    await flushPromises()

    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
    // 菜单仍开着：定时轮询照常跳过。
    expect(wrapper.findComponent({ name: 'AccountColumnSettings' }).exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })

  it('stops polling after the page is left', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(60_000)

    expect(getUpstreamBalances).toHaveBeenCalledTimes(1)
  })
})
