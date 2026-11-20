import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterAll, afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
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
enableAutoUnmount(afterEach)

const slots = {
  info: () => h('span', '20+ items'),
  actions: () => h('button', { type: 'button', 'data-test': 'refresh' }, 'refresh'),
  sheet: () => h('div', { 'data-test': 'fields' }),
  filters: () => h('div', { 'data-test': 'inline-filters' })
}
const mountAt = (at: number, props: Record<string, unknown> = {}, extra: Record<string, () => unknown> = {}) => {
  width = at
  return mount(TableToolbar, {
    attachTo: document.body,
    props: { compact: true, filterSheet: true, filterCount: 2, ...props },
    slots: { ...slots, ...extra },
    global: { stubs: { Icon: true, BottomSheet: { props: ['show', 'testId'], template: '<div v-if="show" role="dialog"><div :data-test="testId"><slot /></div><slot name="footer" /></div>' } } }
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

describe('TableToolbar keeps the focus across its switch', () => {
  const fields = {
    sheet: () => [h('label', { 'data-filter': 'model' }, [h('input', { 'data-test': 'sheet-model' })]), h('input', { 'data-filter': 'status', 'data-test': 'sheet-status' })],
    filters: () => h('div', [h('button', { type: 'button', 'data-filter': 'model', 'data-test': 'inline-model' }), h('button', { type: 'button', 'data-filter': 'status', 'data-test': 'inline-status' })])
  }
  const active = () => (document.activeElement as HTMLElement | null)?.dataset.test

  it('moves from a sheet field to its inline twin when the sheet closes at 1024px', async () => {
    const wrapper = mountAt(1023, { sheetBelow: 'desktop' }, fields)
    await wrapper.get('[data-test="toolbar-filter-button"]').trigger('click')
    ;(wrapper.get('[data-test="sheet-status"]').element as HTMLElement).focus()
    setWidth(1024)
    await flushPromises()
    expect(active()).toBe('inline-status')
    wrapper.unmount()
  })

  it('moves to the first inline filter when the focused field has no twin, and to "Filters" going back', async () => {
    const wrapper = mountAt(1023, { sheetBelow: 'desktop' }, { ...fields, sheet: () => h('input', { 'data-test': 'sheet-other' }) })
    await wrapper.get('[data-test="toolbar-filter-button"]').trigger('click')
    ;(wrapper.get('[data-test="sheet-other"]').element as HTMLElement).focus()
    setWidth(1100)
    await flushPromises()
    expect(active()).toBe('inline-model')
    setWidth(900)
    await flushPromises()
    expect(active()).toBe('toolbar-filter-button')
    wrapper.unmount()
  })

  it('moves from the sheet footer to the first inline filter', async () => {
    const wrapper = mountAt(1023, { sheetBelow: 'desktop' }, fields)
    await wrapper.get('[data-test="toolbar-filter-button"]').trigger('click')
    ;(wrapper.get('[data-test="filter-sheet-done"]').element as HTMLElement).focus()
    setWidth(1024)
    await flushPromises()
    expect(active()).toBe('inline-model')
  })

  it('leaves the focus alone when it was not in the toolbar', async () => {
    const outside = document.body.appendChild(Object.assign(document.createElement('button'), { type: 'button' }))
    outside.dataset.test = 'outside'
    const wrapper = mountAt(1023, { sheetBelow: 'desktop' }, fields)
    outside.focus()
    setWidth(1024)
    await flushPromises()
    expect(active()).toBe('outside')
    wrapper.unmount()
    outside.remove()
  })
})

// Astra post-acceptance review #2: at 320px the merged info row put "Filters" over the live status. The row wraps and
// the actions take the next line, on the right (measured in Chrome; jsdom lays nothing out).
describe('TableToolbar phone info row', () => {
  it('wraps, the actions to a line of their own on the right, wrapping within it if they must', () => {
    const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../TableToolbar.vue'), 'utf8')
    expect(source).toMatch(/\.table-toolbar--phone \.table-toolbar__info-row \{\s*display: flex;\s*flex-wrap: wrap;/)
    expect(source).toMatch(/\.table-toolbar--phone \.table-toolbar__actions \{\s*flex: 0 1 auto;\s*flex-wrap: wrap;\s*margin-left: auto;/)
  })
})

