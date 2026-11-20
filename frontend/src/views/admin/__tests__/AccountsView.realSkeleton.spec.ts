import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

// Two combined regressions from the review (Astra): in the page's real skeleton (layout, toolbar, bulk bar, pagination),
// ticking a second row after the first; and a sort that survives a change of width and a reload: the request, the
// headers and the stored preference agree throughout.

const { listAccounts, backendSummary, todayStats, getById } = vi.hoisted(() => ({
  getById: vi.fn(async (id: number) => ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', group_ids: [53] })),
  listAccounts: vi.fn(),
  backendSummary: vi.fn(),
  todayStats: vi.fn()
}))
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ manage: true })
})

vi.mock('@/api/admin/schedQuality', () => ({ default: { accountDetail: vi.fn(() => new Promise(() => {})), groupBoard: vi.fn(), getGroupPolicy: vi.fn() }, backendSummary }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      getById,
      getBatchTodayStats: todayStats,
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      getUpstreamBalances: vi.fn().mockResolvedValue({ balances: [] }),
      getBatchUsage: vi.fn().mockResolvedValue({ usage: {}, errors: {} })
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: { getAll: vi.fn().mockResolvedValue([{ id: 53, name: '晨星组', platform: 'openai', sort_order: 0 }]) }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ token: 'test-token', isSimpleMode: false, isAdmin: false }) }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => perms.manage), has: () => true, isSuperAdmin: computed(() => true) }) }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true }) }
})

const ROWS = [1, 2, 3].map((id) => ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53] }))

/** A viewport whose width can change: min-width queries follow it, a mouse pointer. */
let width = 1180
const listeners = new Set<() => void>()
const setWidth = async (next: number) => {
  width = next
  listeners.forEach((listener) => listener())
  await flushPromises()
}
const matches = (query: string) => {
  const min = /min-width:\s*(\d+)px/.exec(query)
  if (min) return width >= Number(min[1])
  return query.includes('pointer: fine') || query.includes('hover: hover')
}
const matchMedia = window.matchMedia
beforeEach(() => {
  perms.manage = true
  width = 1180
  listeners.clear()
  localStorage.clear()
  document.body.innerHTML = ''
  document.documentElement.dataset.motion = 'reduced'
  window.matchMedia = ((query: string) => {
    const list = {
      get matches() { return matches(query) },
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: (_: string, listener: (event: { matches: boolean }) => void) => listeners.add(() => listener({ matches: matches(query) })),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn()
    }
    return list
  }) as unknown as typeof window.matchMedia
  listAccounts.mockReset().mockResolvedValue({ items: ROWS, total: 3, page: 1, page_size: 20, pages: 1 })
  backendSummary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] })
  todayStats.mockReset().mockResolvedValue({ stats: {} })
})
afterAll(() => {
  window.matchMedia = matchMedia
  delete document.documentElement.dataset.motion
})

const mountView = () => mount(AccountsView, { attachTo: document.body, global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } } })
const row = (id: number) => document.querySelector<HTMLElement>(`tbody tr[data-row-id="${id}"]`)!
const lastSort = () => {
  const params = listAccounts.mock.calls.at(-1)?.[2] as { sort_by?: string; sort_order?: string } | undefined
  return [params?.sort_by, params?.sort_order]
}
const ariaSort = () => Object.fromEntries(Array.from(document.querySelectorAll<HTMLElement>('thead th[aria-sort]')).map((th) => [[...th.classList].find((cls) => cls.startsWith('acol-'))!.slice(5), th.getAttribute('aria-sort')]))

describe('AccountsView in its real skeleton', () => {
  it('ticks a second row after the first, with the bulk bar shown above the table', async () => {
    await setWidth(390)
    const wrapper = mountView()
    await flushPromises()
    row(1).querySelector<HTMLInputElement>('td.acol-select input')!.click()
    await flushPromises()
    expect(document.querySelector('[data-test="bulk-actions"]')).not.toBeNull()
    row(2).querySelector<HTMLInputElement>('td.acol-select input')!.click()
    await flushPromises()
    expect([1, 2, 3].map((id) => row(id).classList.contains('is-selected'))).toEqual([true, true, false])
    wrapper.unmount()
  })

  it('keeps a sort through a change of width and a reload: request, headers and stored preference agree', async () => {
    // Wide: ID and last used shown; the phone hides ID.
    localStorage.setItem('account-hidden-columns-v2', '[]')
    localStorage.setItem('account-hidden-columns-phone-v1', JSON.stringify(['id', 'platform_type', 'priority', 'usage', 'proxy', 'rate_multiplier', 'upstream_balance', 'created_at', 'expires_at', 'notes', 'iq_check', 'capacity', 'upstream_billing_rate', 'today_stats']))
    const wrapper = mountView()
    await flushPromises()
    document.querySelector<HTMLElement>('[data-test="sort-id"]')!.click()
    await flushPromises()
    expect(lastSort()).toEqual(['id', 'asc'])
    expect(ariaSort()).toMatchObject({ id: 'ascending', last_used_at: 'none' })
    expect(JSON.parse(localStorage.getItem('account-table-sort-v2')!)).toEqual({ key: 'id', order: 'asc' })

    await setWidth(390)
    expect(lastSort()).toEqual(['id', 'asc'])
    expect(ariaSort()).toMatchObject({ last_used_at: 'none' })
    expect(ariaSort().id).toBeUndefined()
    expect(document.querySelector('[data-test="table-sort-hint"]')!.textContent).toContain('admin.accounts.columns.id')
    expect(JSON.parse(localStorage.getItem('account-table-sort-v2')!)).toEqual({ key: 'id', order: 'asc' })

    await setWidth(1180)
    expect(ariaSort()).toMatchObject({ id: 'ascending', last_used_at: 'none' })
    wrapper.unmount()

    // A reload on the phone asks for the same sort.
    listAccounts.mockClear()
    await setWidth(390)
    const again = mountView()
    await flushPromises()
    expect(lastSort()).toEqual(['id', 'asc'])
    expect(ariaSort()).toMatchObject({ last_used_at: 'none' })
    again.unmount()
  })
})
