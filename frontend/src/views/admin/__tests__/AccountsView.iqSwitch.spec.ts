import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'

// The IQ switch in the list behaves like the scheduling switch: it flips at once, is busy until the answer and goes
// back on a failure (7e-1).

const { listAccounts, setIQCheck, showError } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  setIQCheck: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin/schedQuality', () => ({ backendSummary: vi.fn().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] }), default: {} }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      setIQCheck,
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} }),
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      getUpstreamBalances: vi.fn().mockResolvedValue({ items: [] })
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([]) }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token', isSimpleMode: false }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DataTableStub = defineComponent({
  props: ['columns', 'data'],
  setup(props, { slots }) {
    return () => h('div', (props.data as Array<{ id: number }>).map((row) => h('div', { 'data-id': row.id }, [slots['cell-iq_check']?.({ row })])))
  }
})

const mountView = () => mount(AccountsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
      DataTable: DataTableStub,
      AccountsToolbar: true,
      AccountBulkActionsBar: true,
      Pagination: true,
      ConfirmDialog: true,
      Icon: true,
      Teleport: true
    }
  }
})

const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const target = (model: string, enabled: boolean) =>
  ({ model, enabled, verdict: 'unknown', state: 'idle', gate_active: false, verdict_at: null, last_run_at: null, next_run_at: null })

describe('admin AccountsView IQ switch', () => {
  beforeEach(() => {
    localStorage.clear()
    showError.mockReset()
    setIQCheck.mockReset()
    // An OpenAI account that has never had IQ checks: no targets at all.
    listAccounts.mockReset().mockResolvedValue({
      items: [{ id: 5, name: 'acc-5', platform: 'openai', type: 'oauth', status: 'active', schedulable: true, iq_targets: [] }],
      total: 1, page: 1, page_size: 20, pages: 1
    })
  })

  it('turns on before the answer, busy until it is in, for an account without targets yet', async () => {
    const answer = deferred<unknown>()
    setIQCheck.mockReturnValue(answer.promise)
    const wrapper = mountView()
    await flushPromises()
    const toggle = () => wrapper.get('[data-testid="iq-check-cell"] [role="switch"]')
    expect(toggle().attributes('aria-checked')).toBe('false')

    await toggle().trigger('click')
    expect(toggle().attributes('aria-checked')).toBe('true')
    expect(toggle().attributes('aria-busy')).toBe('true')

    answer.resolve([target('gpt-6-astra', true), target('gpt-6.1-sol', true)])
    await flushPromises()
    expect(toggle().attributes('aria-checked')).toBe('true')
    expect(toggle().attributes('aria-busy')).toBeUndefined()
    wrapper.unmount()
  })

  it('goes back when the request fails, and says why', async () => {
    const answer = deferred<unknown>()
    setIQCheck.mockReturnValue(answer.promise)
    const wrapper = mountView()
    await flushPromises()
    const toggle = () => wrapper.get('[data-testid="iq-check-cell"] [role="switch"]')
    await toggle().trigger('click')
    expect(toggle().attributes('aria-checked')).toBe('true')

    answer.reject({ status: 500, message: 'iq down' })
    await flushPromises()
    expect(toggle().attributes('aria-checked')).toBe('false')
    expect(toggle().attributes('aria-busy')).toBeUndefined()
    expect(showError).toHaveBeenCalledWith('admin.accounts.failedWithReason')
    wrapper.unmount()
  })
})
