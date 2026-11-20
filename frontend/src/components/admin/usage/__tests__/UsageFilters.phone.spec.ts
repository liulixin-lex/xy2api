// Mobile list plan, usage records (B): below 768px the admin usage filters become the shared phone toolbar — "Filters
// n" opens every field in a bottom sheet (applied at once), the chosen ones show as removable tags, the count, refresh,
// columns and "more" (reset, cleanup, export) sit on one row. From 768px the filter row stays as it was.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import UsageFilters from '../UsageFilters.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: { searchUsers: vi.fn().mockResolvedValue([]), searchApiKeys: vi.fn().mockResolvedValue([]) },
    groups: { list: vi.fn().mockResolvedValue({ items: [{ id: 7, name: 'Main' }] }) },
    accounts: { list: vi.fn().mockResolvedValue({ items: [] }) },
  },
}))

function setPhone(phone: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
    matches: query.includes('min-width') ? !phone : true,
    media: query, onchange: null,
    addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
  }) as unknown as MediaQueryList)
}

const filtersWith = (patch: Record<string, unknown> = {}) => ({
  user_id: undefined, api_key_id: undefined, account_id: undefined, model: undefined, request_type: undefined,
  native_compaction_v2: null, billing_type: null, billing_mode: undefined, result: null, upstream_model_mismatch: undefined,
  group_id: undefined, start_date: '2026-11-19', end_date: '2026-11-20', ...patch,
})

function mountFilters(props: Record<string, unknown> = {}, modelValue = filtersWith()) {
  return mount(UsageFilters, {
    props: { modelValue, exporting: false, startDate: '2026-11-19', endDate: '2026-11-20', modelOptions: ['gpt-6.1'], ...props },
    slots: { info: '<span data-test="count">12 rows</span>', 'phone-tools': '<button data-test="columns">columns</button>' },
    global: { stubs: { Select: true, Teleport: true } },
    attachTo: document.body,
  })
}

beforeEach(() => { document.body.innerHTML = '' })
afterEach(() => { vi.restoreAllMocks() })

/** A width: the min-width queries (768px, 1024px) answer for it. */
function setWidth(width: number) {
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => {
    const min = /min-width:\s*(\d+)px/.exec(query)
    const max = /max-width:\s*(\d+)px/.exec(query)
    return {
      matches: min ? width >= Number(min[1]) : max ? width <= Number(max[1]) : true,
      media: query, onchange: null,
      addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn(),
    } as unknown as MediaQueryList
  })
}

describe('UsageFilters below 768px', () => {
  it('keeps the filter row from 1024px', () => {
    setWidth(1024)
    const w = mountFilters()
    expect(w.find('[data-testid="usage-filters-phone"]').exists()).toBe(false)
    expect(w.find('input[placeholder="admin.usage.searchUserPlaceholder"]').exists()).toBe(true)
    expect(w.text()).toContain('common.refresh')
  })

  // Browser acceptance (step 1), items 3 and 4: on a tablet the long form was four rows above the table; below 1024px
  // it is in the sheet, and with no search "Filters" joins the info row (no row of its own).
  it.each([834, 768, 390])('at %ipx uses the sheet, "Filters" leading the info row', async (width) => {
    setWidth(width)
    const w = mountFilters()
    await flushPromises()
    expect(w.find('[data-testid="usage-filters-phone"]').exists()).toBe(true)
    expect(w.find('input[placeholder="admin.usage.searchUserPlaceholder"]').exists()).toBe(false)
    expect(w.find('[data-test="toolbar-first-row"]').exists()).toBe(false)
    const actions = w.get('.table-toolbar__info-row .table-toolbar__actions')
    expect(actions.element.firstElementChild!.getAttribute('data-test')).toBe('toolbar-filter-button')
    expect(w.get('.table-toolbar__info-row').find('[data-test="count"]').exists()).toBe(true)
  })

  it('shows the phone toolbar with the fields only in the sheet, and the page\'s count and tools on the info row', async () => {
    setPhone(true)
    const w = mountFilters()
    await flushPromises()
    expect(w.find('[data-testid="usage-filters-phone"]').exists()).toBe(true)
    expect(w.find('input[placeholder="admin.usage.searchUserPlaceholder"]').exists()).toBe(false)
    expect(w.get('[data-test="count"]').text()).toBe('12 rows')
    expect(w.find('[data-test="columns"]').exists()).toBe(true)
    expect(w.find('[data-test="toolbar-filter-count"]').exists()).toBe(false)

    await w.get('[data-test="toolbar-filter-button"]').trigger('click')
    await nextTick()
    const sheet = w.get('[data-test="filter-sheet"]')
    expect(sheet.find('input[placeholder="admin.usage.searchUserPlaceholder"]').exists()).toBe(true)
    expect(sheet.find('input[placeholder="admin.usage.searchUserPlaceholder"]').classes()).toContain('[@media(pointer:coarse)]:text-base')
  })

  it('counts and tags the chosen filters, and removes one at once', async () => {
    setPhone(true)
    const model = filtersWith({ model: 'gpt-6.1', billing_type: 1, result: 2, group_id: 7 })
    const w = mountFilters({}, model)
    await flushPromises()
    expect(w.get('[data-test="toolbar-filter-count"]').text()).toBe('4')
    const tags = () => w.findAll('[data-testid="usage-filter-tag"]').map((tag) => tag.text())
    expect(tags()).toEqual([
      'usage.model：gpt-6.1',
      'admin.usage.billingType：admin.usage.billingTypeSubscription',
      'admin.usage.result：admin.usage.resultRescued',
      'admin.usage.group：Main',
    ])
    await w.get('[data-key="model"] button').trigger('click')
    expect(model.model).toBeUndefined()
    expect(w.emitted('change')).toHaveLength(1)
    expect(tags()).toHaveLength(3)
  })

  // Review item 6: a tag names what is filtered on, not what is typed in the box afterwards.
  it('names the chosen user, not the search draft typed after choosing', async () => {
    setPhone(true)
    const model = filtersWith({ user_id: 7, account_id: 3 })
    const w = mountFilters({}, model)
    ;(w.vm as unknown as { setUserKeyword: (email: string) => void; setAccountKeyword: (name: string) => void }).setUserKeyword('seven@example.com')
    ;(w.vm as unknown as { setAccountKeyword: (name: string) => void }).setAccountKeyword('acc-3')
    await nextTick()
    const tags = () => w.findAll('[data-testid="usage-filter-tag"]').map((tag) => tag.text())
    expect(tags()).toEqual(['admin.usage.userFilter：seven@example.com', 'admin.usage.account：acc-3'])
    await w.get('[data-test="toolbar-filter-button"]').trigger('click')
    await nextTick()
    const sheet = w.get('[data-test="filter-sheet"]')
    await sheet.get('input[placeholder="admin.usage.searchUserPlaceholder"]').setValue('other')
    await sheet.get('input[placeholder="admin.usage.searchAccountPlaceholder"]').setValue('draft')
    expect(tags()).toEqual(['admin.usage.userFilter：seven@example.com', 'admin.usage.account：acc-3'])
    expect(model.user_id).toBe(7)
  })

  it('clears every chosen filter from the sheet in one change, keeping the date range', async () => {
    setPhone(true)
    const model = filtersWith({ model: 'gpt-6.1', request_type: 'stream', native_compaction_v2: true })
    const w = mountFilters({}, model)
    await w.get('[data-test="toolbar-filter-button"]').trigger('click')
    await nextTick()
    await w.get('[data-test="filter-sheet-clear"]').trigger('click')
    expect(model).toMatchObject({ model: undefined, request_type: undefined, native_compaction_v2: null, start_date: '2026-11-19', end_date: '2026-11-20' })
    expect(w.emitted('change')).toHaveLength(1)
    expect(w.find('[data-testid="usage-filter-tag"]').exists()).toBe(false)
  })

  it('tags the errors tab\'s own filters only', async () => {
    setPhone(true)
    const w = mountFilters({ mode: 'errors' }, filtersWith({ billing_type: 1, status_code: 429, error_category: 'rate_limit' }))
    await flushPromises()
    expect(w.findAll('[data-testid="usage-filter-tag"]').map((tag) => tag.attributes('data-key'))).toEqual(['error_category', 'status_code'])
  })

  it.each([
    ['usage', { canCleanup: true, canExport: true }, ['common.reset', 'admin.usage.cleanup.button', 'usage.exportExcel']],
    ['usage', { canCleanup: false, canExport: false }, ['common.reset']],
    ['errors', { canCleanup: true, canExport: true }, ['common.reset']],
  ] as const)('puts reset, cleanup and export under "more" as the %s tab and permissions allow', async (mode, perms, expected) => {
    setPhone(true)
    const w = mountFilters({ mode, ...perms })
    await w.get('[data-testid="usage-more"]').trigger('click')
    await nextTick()
    const sheet = w.get('[data-test="usage-more-sheet"]')
    expect(sheet.findAll('button').map((b) => b.text())).toEqual(expected)
    await sheet.findAll('button')[0].trigger('click')
    expect(w.emitted('reset')).toHaveLength(1)
  })
})

// Shared layer's review fix (data-filter): crossing 1024px keeps the focus. These filters swap the shared toolbar for
// their own row, so the page hands it over itself: from the sheet to the row's field with the same key, from the row
// to "Filters"; focus elsewhere stays.
describe('UsageFilters keeps the focus across 1024px', () => {
  // A matchMedia whose answers follow the width and tell their listeners.
  const media = (() => {
    let width = 834
    const lists: { query: string; listeners: Set<(e: { matches: boolean }) => void> }[] = []
    const matches = (q: string) => {
      const min = /min-width:\s*(\d+)px/.exec(q)
      const max = /max-width:\s*(\d+)px/.exec(q)
      return min ? width >= Number(min[1]) : max ? width <= Number(max[1]) : true
    }
    return {
      install(start: number) {
        width = start
        lists.length = 0
        vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => {
          const entry = { query, listeners: new Set<(e: { matches: boolean }) => void>() }
          lists.push(entry)
          return {
            get matches() { return matches(query) }, media: query, onchange: null,
            addEventListener: (_: string, fn: (e: { matches: boolean }) => void) => entry.listeners.add(fn),
            removeEventListener: (_: string, fn: (e: { matches: boolean }) => void) => entry.listeners.delete(fn),
            addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn(),
          } as unknown as MediaQueryList
        })
      },
      async set(next: number) {
        width = next
        for (const entry of lists) for (const fn of entry.listeners) fn({ matches: matches(entry.query) })
        await flushPromises()
        await nextTick()
      },
    }
  })()

  it('moves focus from a sheet field to the same field in the row, and from the row to "Filters"', async () => {
    media.install(834)
    const w = mountFilters()
    await flushPromises()
    await w.get('[data-test="toolbar-filter-button"]').trigger('click')
    await flushPromises()
    const inSheet = document.querySelector<HTMLInputElement>('[data-test="filter-sheet"] [data-filter="account_id"] input')!
    inSheet.focus()
    expect(document.activeElement).toBe(inSheet)
    await media.set(1100)
    expect(w.find('[data-testid="usage-filters-phone"]').exists()).toBe(false)
    const active = document.activeElement as HTMLElement
    expect(active.tagName).toBe('INPUT')
    expect(active.closest('[data-filter]')?.getAttribute('data-filter')).toBe('account_id')
    expect(w.element.contains(active)).toBe(true)
    await media.set(834)
    expect((document.activeElement as HTMLElement).getAttribute('data-test')).toBe('toolbar-filter-button')
    w.unmount()
  })

  it('leaves focus elsewhere on the page alone', async () => {
    media.install(1100)
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    const w = mountFilters()
    await flushPromises()
    outside.focus()
    await media.set(834)
    expect(document.activeElement).toBe(outside)
    w.unmount()
  })
})
