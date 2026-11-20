import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

import AccountsView from '../AccountsView.vue'

// The accounts page on a phone (mobile list plan): the desktop table with the phone's default columns, the name and
// "⋯" frozen, editing in the row menu. The group editing, menu placement and rights the cards had are kept on the row.

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

const ROWS = [
  ...[1, 2].map((id) => ({ id, name: `acc-${id}`, platform: 'openai', type: 'oauth', status: 'active', schedulable: true, group_ids: [53] })),
  { id: 3, name: 'acc-3', platform: 'anthropic', type: 'apikey', status: 'active', schedulable: true, group_ids: [] }
]

/** The toolbar's stand-in: its props, and the phone's "Columns" button. */
const toolbarProps: Record<string, unknown> = {}
const ToolbarStub = defineComponent({
  props: ['sortHint', 'total'],
  emits: ['columns'],
  setup(props, { emit }) {
    return () => {
      Object.assign(toolbarProps, { sortHint: props.sortHint, total: props.total })
      return h('button', { 'data-test': 'columns-button', onClick: () => emit('columns') })
    }
  }
})

function mountView() {
  return mount(AccountsView, {
    attachTo: document.body,
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        AccountsToolbar: ToolbarStub,
        AccountBulkActionsBar: true,
        Pagination: true,
        Icon: true
      }
    }
  })
}
const row = (id: number) => document.querySelector<HTMLElement>(`tbody tr[data-row-id="${id}"]`)!
const headerKeys = () => Array.from(document.querySelectorAll<HTMLElement>('thead th')).map((th) => [...th.classList].find((cls) => cls.startsWith('acol-'))!.slice(5))

const matchMedia = window.matchMedia
beforeEach(() => {
  perms.manage = true
  localStorage.clear()
  document.body.innerHTML = ''
  document.documentElement.dataset.motion = 'reduced'
  // A phone: under 768px wide (every min-width query fails), a mouse-like pointer.
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
  listAccounts.mockReset().mockResolvedValue({ items: ROWS, total: 3, page: 1, page_size: 20, pages: 1 })
  backendSummary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] })
  todayStats.mockReset().mockResolvedValue({ stats: {} })
  getById.mockClear()
})
afterAll(() => {
  window.matchMedia = matchMedia
  delete document.documentElement.dataset.motion
})

describe('AccountsView on a phone (mobile list plan)', () => {
  it('shows the table with the phone\'s default columns; the row ends in a frozen "⋯" only', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(document.querySelector('[data-test="account-card"]')).toBeNull()
    expect(headerKeys()).toEqual(['select', 'name', 'status', 'schedulable', 'groups', 'sq_recent', 'actions'])
    expect(row(2).querySelector('[data-test="row-edit"]')).toBeNull()
    expect(row(2).querySelector('td.acol-actions [data-test="row-more"]')).not.toBeNull()
    expect(row(2).querySelector('td.acol-actions')!.classList).toContain('sticky-col-right')
    expect(row(2).querySelector('td.acol-name')!.classList).toContain('sticky-col-left-second')
    // The sort's column (last used) is hidden, so the toolbar says it.
    expect(toolbarProps.sortHint).toEqual({ label: 'admin.accounts.columns.lastUsed', order: 'desc' })
    wrapper.unmount()
  })

  it('opens editing from the row menu, its first item', async () => {
    const wrapper = mountView()
    await flushPromises()
    row(1).querySelector<HTMLElement>('[data-test="row-more"]')!.click()
    await flushPromises()
    const items = Array.from(document.querySelectorAll<HTMLElement>('.action-menu-content button'))
    expect(items[0].dataset.test).toBe('menu-edit')
    items[0].click()
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(1)
    wrapper.unmount()
  })

  it('says why a non-OpenAI account has no recent status, with one "—"', async () => {
    const wrapper = mountView()
    await flushPromises()
    const cell = row(3).querySelector('td.acol-sq_recent')!
    expect(cell.querySelector('[data-test="account-recent"]')).toBeNull()
    const empty = cell.querySelector<HTMLElement>('[data-test="cell-empty"]')!
    expect(empty.querySelector('[aria-hidden="true"]')!.textContent).toBe('—')
    expect(empty.querySelector('.sr-only')!.textContent).toBe('admin.accounts.table.recent.openaiOnly')
    // Not a tab stop (Opus review, suggestion 6).
    expect(empty.hasAttribute('tabindex')).toBe(false)
    expect(row(1).querySelector('td.acol-sq_recent [data-test="account-recent"]')).not.toBeNull()
    wrapper.unmount()
  })

  it('remembers the phone\'s columns apart from the wider screens\' (its own key; the old key untouched)', async () => {
    localStorage.setItem('account-hidden-columns-v2', JSON.stringify(['id']))
    const wrapper = mountView()
    await flushPromises()
    document.querySelector<HTMLElement>('[data-test="columns-button"]')!.click()
    await flushPromises()
    const sheet = document.querySelector<HTMLElement>('[data-test="column-sheet"]')!
    const toggle = Array.from(sheet.querySelectorAll<HTMLElement>('[data-test="column-setting"] button')).find((button) => button.textContent!.includes('admin.accounts.iqCheck'))!
    toggle.click()
    await flushPromises()
    expect(headerKeys()).toContain('iq_check')
    expect(JSON.parse(localStorage.getItem('account-hidden-columns-phone-v1')!)).not.toContain('iq_check')
    expect(JSON.parse(localStorage.getItem('account-hidden-columns-v2')!)).toEqual(['id'])
    // This width's defaults back; the order (shared) has its own reset.
    sheet.querySelector<HTMLElement>('[data-test="reset-columns"]')!.click()
    await flushPromises()
    expect(headerKeys()).not.toContain('iq_check')
    expect(localStorage.getItem('account-hidden-columns-phone-v1')).toBeNull()
    expect(sheet.querySelector('[data-test="reset-column-order"]')).toBeNull()
    wrapper.unmount()
  })

  it('marks a ticked row in the selected background on a phone too', async () => {
    const wrapper = mountView()
    await flushPromises()
    const box = row(2).querySelector<HTMLInputElement>('td.acol-select input[type="checkbox"]')!
    box.click()
    await flushPromises()
    expect(row(2).classList).toContain('is-selected')
    expect(row(1).classList).not.toContain('is-selected')
    wrapper.unmount()
  })

  it('edits the groups from the row menu, beside the row\'s groups cell; focus goes back to "⋯" (spec 4.3, 7l-4)', async () => {
    const wrapper = mountView()
    await flushPromises()
    row(1).querySelector<HTMLElement>('[data-test="row-more"]')!.click()
    await flushPromises()
    document.querySelector<HTMLElement>('[data-test="menu-edit-groups"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-group-picker]')!.getAttribute('aria-label')).toContain('acc-1')
    document.querySelector<HTMLElement>('[data-group-picker] input')!.focus()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(document.querySelector('[data-group-picker]')).toBeNull()
    expect(document.activeElement).toBe(row(1).querySelector('[data-test="row-more"]'))
    wrapper.unmount()
  })

  it('keeps an open row menu at its button when a live update changes the height of the rows above (7m-3)', async () => {
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
    const button = row(2).querySelector<HTMLElement>('[data-test="row-more"]')!
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

  it('offers no editing and no group editing without the right to manage accounts', async () => {
    perms.manage = false
    const wrapper = mountView()
    await flushPromises()
    row(1).querySelector<HTMLElement>('[data-test="row-more"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-test="menu-edit-groups"]')).toBeNull()
    expect(document.querySelector('[data-test="menu-edit"]')).toBeNull()
    wrapper.unmount()
  })

  // Astra third re-review #3: a tap on the today cell's padding showed nothing; the padding moves inside its target.
  it('makes the whole today cell, its padding included, the tap target', () => {
    const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../AccountsView.vue'), 'utf8')
    expect(source).toMatch(/:deep\(td\.acol-today_stats\) \{\s*padding: 0;\s*\}/)
    expect(source).toMatch(/:deep\(td\.acol-today_stats \.today\) \{\s*flex: 1 1 auto;\s*align-content: center;\s*padding: 0 var\(--cell-pad-x\);/)
  })
})
