import { afterAll, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'

// The accounts page below 1024px (spec 4.6): its own two-layer cards on the shared card view.

const { listAccounts, backendSummary, todayStats } = vi.hoisted(() => ({
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
      getById: vi.fn(async (id: number) => ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', group_ids: [53] })),
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

const ROWS = [1, 2].map((id) => ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53] }))

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        AccountsToolbar: defineComponent({ setup: () => () => h('div') }),
        AccountBulkActionsBar: true,
        Pagination: true,
        Icon: true
      }
    }
  })
}
const card = (id: number) => document.querySelectorAll<HTMLElement>('[data-test="account-card"]')[id - 1]
const vm = (wrapper: ReturnType<typeof mountView>) => wrapper.vm as unknown as { reload: () => Promise<void> }

const matchMedia = window.matchMedia
beforeEach(() => {
  perms.manage = true
  localStorage.clear()
  document.body.innerHTML = ''
  document.documentElement.dataset.motion = 'reduced'
  // A phone: not 1024px wide, a mouse-like pointer.
  window.matchMedia = ((query: string) => ({
    matches: query.includes('pointer: fine'),
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn()
  })) as unknown as typeof window.matchMedia
  listAccounts.mockReset().mockResolvedValue({ items: ROWS, total: 2, page: 1, page_size: 20, pages: 1 })
  backendSummary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] })
  todayStats.mockReset().mockResolvedValue({ stats: { 2: { requests: 123456, tokens: 532730000, cost: 1234.5, standard_cost: 1234.5, user_cost: 9995 } } })
})
afterAll(() => {
  window.matchMedia = matchMedia
  delete document.documentElement.dataset.motion
})

describe('AccountsView cards (spec 4.6)', () => {
  it('shows name, status, switch, groups and recent status at once, the details on demand per account, open across a reload', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(document.querySelectorAll('[data-test="account-card"]')).toHaveLength(2)
    const second = card(2)
    expect(second.querySelector('[data-test="account-name"]')!.textContent).toBe('acc-2')
    for (const part of ['account-status', 'group-pills', 'account-recent']) expect(second.querySelector(`[data-test="${part}"]`)).not.toBeNull()
    expect(second.querySelector('[role="switch"], [aria-checked]')).not.toBeNull()
    expect(second.querySelector('[data-test="account-card-more"]')).toBeNull()

    second.querySelector<HTMLElement>('[data-test="account-card-toggle"]')!.click()
    await flushPromises()
    const fields = Array.from(card(2).querySelectorAll<HTMLElement>('[data-test="account-card-more"] [data-field]')).map((el) => el.dataset.field)
    expect(fields).toEqual(['iq_check', 'capacity', 'upstream_billing_rate', 'today_stats'])
    // Touch has no hover: the details give today's full values, not the short forms.
    expect(card(2).querySelector('[data-field="today_stats"]')!.textContent).toContain('532,730,000')

    await vm(wrapper).reload()
    await flushPromises()
    expect(card(2).querySelector('[data-test="account-card-more"]')).not.toBeNull()
    expect(card(1).querySelector('[data-test="account-card-more"]')).toBeNull()
    wrapper.unmount()
  })

  it('edits the groups from the card\'s button and from the row menu, beside the card\'s groups (spec 4.3)', async () => {
    const wrapper = mountView()
    await flushPromises()
    card(2).querySelector<HTMLElement>('[data-test="card-edit-groups"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-group-picker]')!.getAttribute('aria-label')).toContain('acc-2')
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    await flushPromises()
    expect(document.querySelector('[data-group-picker]')).toBeNull()

    card(1).querySelector<HTMLElement>('[data-test="row-more"]')!.click()
    await flushPromises()
    document.querySelector<HTMLElement>('[data-test="menu-edit-groups"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-group-picker]')!.getAttribute('aria-label')).toContain('acc-1')
    // Closed from inside, it gives focus back to the "more" button it came from (7l-4).
    document.querySelector<HTMLElement>('[data-group-picker] input')!.focus()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(document.querySelector('[data-group-picker]')).toBeNull()
    expect(document.activeElement).toBe(card(1).querySelector('[data-test="row-more"]'))
    wrapper.unmount()
  })

  it('keeps an open row menu at its button when a live update changes the height of the cards above (7m-3)', async () => {
    const observers: Array<() => void> = []
    vi.stubGlobal('ResizeObserver', class {
      private targets: Element[] = []
      constructor(callback: ResizeObserverCallback) {
        observers.push(() => callback(this.targets.map((target) => ({ target, contentRect: target.getBoundingClientRect() }) as ResizeObserverEntry), this as unknown as ResizeObserver))
      }
      observe(target: Element) { this.targets.push(target) }
      unobserve() {}
      disconnect() { this.targets = [] }
    })
    onTestFinished(() => vi.unstubAllGlobals())
    const wrapper = mountView()
    await flushPromises()
    const button = card(2).querySelector<HTMLElement>('[data-test="row-more"]')!
    let top = 300
    button.getBoundingClientRect = () => ({ top, bottom: top + 28, left: 300, right: 328, width: 28, height: 28, x: 300, y: top, toJSON: () => ({}) }) as DOMRect
    button.click()
    await flushPromises()
    const menuTop = () => document.querySelector<HTMLElement>('.action-menu-content')!.style.top
    expect(menuTop()).toBe('332px')

    top = 360
    observers.forEach((fire) => fire())
    await flushPromises()
    expect(menuTop()).toBe('392px')
    wrapper.unmount()
  })

  it('offers no group editing without the right to manage accounts', async () => {
    perms.manage = false
    const wrapper = mountView()
    await flushPromises()
    expect(card(1).querySelector('[data-test="card-edit-groups"]')).toBeNull()
    card(1).querySelector<HTMLElement>('[data-test="row-more"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-test="menu-edit-groups"]')).toBeNull()
    wrapper.unmount()
  })
})
