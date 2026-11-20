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

describe('UsageFilters below 768px', () => {
  it('keeps the filter row from 768px', () => {
    setPhone(false)
    const w = mountFilters()
    expect(w.find('[data-testid="usage-filters-phone"]').exists()).toBe(false)
    expect(w.find('input[placeholder="admin.usage.searchUserPlaceholder"]').exists()).toBe(true)
    expect(w.text()).toContain('common.refresh')
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
