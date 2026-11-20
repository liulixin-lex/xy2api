import { afterAll, afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { h, nextTick } from 'vue'

import TableToolbar from '../TableToolbar.vue'

// The phone toolbar's extent and its first row (mobile list plan, usage records): `sheetBelow="desktop"` keeps the
// phone toolbar below 1024px; a page with neither #search nor #create has no first row, "Filters" leads the actions.

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key) }) }))

type Listener = (event: { matches: boolean }) => void
const listeners = new Map<Listener, () => boolean>()
let width = 1440
const setWidth = (next: number) => {
  width = next
  listeners.forEach((matches, listener) => listener({ matches: matches() }))
}
const original = window.matchMedia
window.matchMedia = ((query: string) => {
  const min = Number(/min-width:\s*(\d+)px/.exec(query)?.[1] ?? NaN)
  const matches = () => (Number.isNaN(min) ? false : width >= min)
  return {
    get matches() { return matches() },
    media: query,
    addEventListener: (_: string, listener: Listener) => listeners.set(listener, matches),
    removeEventListener: (_: string, listener: Listener) => listeners.delete(listener),
    addListener: () => {},
    removeListener: () => {}
  }
}) as unknown as typeof window.matchMedia
afterEach(() => {
  listeners.clear()
  width = 1440
})
afterAll(() => { window.matchMedia = original })

const slots = {
  info: () => h('span', '20+ items'),
  actions: () => h('button', { type: 'button', 'data-test': 'refresh' }, 'refresh'),
  sheet: () => h('div', { 'data-test': 'fields' }),
  filters: () => h('div', { 'data-test': 'inline-filters' })
}
const mountAt = (at: number, props: Record<string, unknown> = {}, extra: Record<string, () => unknown> = {}) => {
  width = at
  return mount(TableToolbar, {
    props: { compact: true, filterSheet: true, filterCount: 2, ...props },
    slots: { ...slots, ...extra },
    global: { stubs: { Icon: true, BottomSheet: { props: ['show'], template: '<div v-if="show" data-test="sheet"><slot /></div>' } } }
  })
}

describe('TableToolbar sheetBelow', () => {
  it('keeps the phone toolbar below 768px by default, the row layout from there', () => {
    expect(mountAt(767).find('[data-test="table-toolbar-phone"]').exists()).toBe(true)
    const tablet = mountAt(768)
    expect(tablet.find('[data-test="table-toolbar-phone"]').exists()).toBe(false)
    expect(tablet.find('[data-test="inline-filters"]').exists()).toBe(true)
  })

  it("keeps the phone toolbar to 1024px with 'desktop', and closes its sheet on crossing it", async () => {
    const wrapper = mountAt(1023, { sheetBelow: 'desktop' })
    expect(wrapper.find('[data-test="table-toolbar-phone"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="inline-filters"]').exists()).toBe(false)
    await wrapper.get('[data-test="toolbar-filter-button"]').trigger('click')
    expect(wrapper.find('[data-test="fields"]').exists()).toBe(true)

    setWidth(1024)
    await nextTick()
    expect(wrapper.find('[data-test="table-toolbar-phone"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="inline-filters"]').exists()).toBe(true)
    setWidth(900)
    await nextTick()
    expect(wrapper.find('[data-test="fields"]').exists()).toBe(false)
  })
})

describe('TableToolbar phone first row', () => {
  it('has no first row without #search and #create: "Filters" leads the info row, count and name as before', async () => {
    const wrapper = mountAt(390)
    expect(wrapper.find('[data-test="toolbar-first-row"]').exists()).toBe(false)
    const actions = wrapper.get('.table-toolbar__info-row .table-toolbar__actions')
    const buttons = actions.findAll('button')
    expect(buttons.map((button) => button.attributes('data-test'))).toEqual(['toolbar-filter-button', 'refresh'])
    const filter = buttons[0]
    expect(filter.classes()).toContain('table-toolbar__btn')
    expect(filter.attributes('aria-label')).toBe('common.table.filtersActive {"n":2}')
    expect(filter.attributes('aria-haspopup')).toBe('dialog')
    expect(filter.get('[data-test="toolbar-filter-count"]').text()).toBe('2')
    await filter.trigger('click')
    expect(filter.attributes('aria-expanded')).toBe('true')
    expect(wrapper.find('[data-test="fields"]').exists()).toBe(true)
  })

  it('keeps the first row, with "Filters" on it, when the page has a search or #create', () => {
    for (const extra of [{ search: () => h('input') }, { create: () => h('button', { type: 'button' }, 'add') }]) {
      const wrapper = mountAt(390, {}, extra)
      const row = wrapper.get('[data-test="toolbar-first-row"]')
      expect(row.find('[data-test="toolbar-filter-button"]').exists()).toBe(true)
      expect(wrapper.findAll('[data-test="toolbar-filter-button"]')).toHaveLength(1)
    }
  })
})
