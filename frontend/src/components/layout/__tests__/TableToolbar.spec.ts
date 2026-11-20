import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createCommentVNode, h, nextTick, ref } from 'vue'

import TableToolbar from '../TableToolbar.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../TableToolbar.vue'), 'utf8')
const narrow = source.match(/@media \(max-width: 1023px\) \{([\s\S]*?)\n\}/)![1]
const wide = source.slice(source.indexOf('<style scoped>'), source.indexOf('@media'))

describe('TableToolbar from 1024px', () => {
  it('bases the main part on its content: the search and the filters keep a row, the actions move to the next as a whole (7r-5)', () => {
    const main = wide.match(/\n\.table-toolbar__main \{([^{}]*)\}/)![1]
    expect(main).toContain('flex: 1 1 auto')
    expect(main).toContain('min-width: 0')
    expect(wide).toMatch(/\n\.table-toolbar \{[^{}]*flex-wrap: wrap;[^{}]*justify-content: flex-end;/)
  })
})

describe('TableToolbar filters', () => {
  it('takes no row and no gap when the filters slot renders nothing, as with every filter switched off (7s-3)', () => {
    const toolbar = (filters: () => unknown[]) => mount(TableToolbar, { slots: { search: () => h('input'), filters } })
    // Every filter behind a false v-if: the slot renders comments only.
    expect(toolbar(() => [createCommentVNode('v-if', true), createCommentVNode('v-if', true)]).find('[data-test="toolbar-filters"]').exists()).toBe(false)
    expect(toolbar(() => [h('select')]).find('[data-test="toolbar-filters"]').exists()).toBe(true)
  })
})

describe('TableToolbar below 1024px', () => {
  it('keeps the column from wrapping, so the filter row scrolls inside itself instead of widening the page (7n-1)', () => {
    // The narrow layout is every toolbar's below 1024px except a filterSheet one (from 768px it keeps the row).
    const column = narrow.match(/\.table-toolbar:where\(:not\(\.table-toolbar--sheet\)\),\s*:where\(\.table-toolbar:not\(\.table-toolbar--sheet\)\) \.table-toolbar__main \{([^{}]*)\}/)![1]
    expect(column).toContain('flex-direction: column')
    expect(column).toContain('flex-wrap: nowrap')
  })

  it('makes the main part as tall as its rows: its wide basis would be about a height there (7q-2)', () => {
    expect(narrow).toMatch(/\n {2}:where\(\.table-toolbar:not\(\.table-toolbar--sheet\)\) \.table-toolbar__main \{\s*flex: none;\s*\}/)
  })
})

// Mobile list plan, item 15: the sideways filter row below 1024px fades out at an edge with more filters past it.
describe('TableToolbar filter row fade', () => {
  it('fades the edges that have filters past them, following the scroll', async () => {
    const wrapper = mount(TableToolbar, { slots: { filters: () => h('div', { class: 'f' }, 'filters') }, attachTo: document.body })
    const row = wrapper.get('[data-test="toolbar-filters"]')
    const el = row.element as HTMLElement
    Object.defineProperty(el, 'clientWidth', { configurable: true, value: 300 })
    Object.defineProperty(el, 'scrollWidth', { configurable: true, value: 700 })
    el.scrollLeft = 0
    await row.trigger('scroll')
    expect(row.classes()).toEqual(expect.arrayContaining(['is-fade-end']))
    expect(row.classes()).not.toContain('is-fade-start')
    el.scrollLeft = 200
    await row.trigger('scroll')
    expect(row.classes()).toEqual(expect.arrayContaining(['is-fade-start', 'is-fade-end']))
    el.scrollLeft = 400
    await row.trigger('scroll')
    expect(row.classes()).toContain('is-fade-start')
    expect(row.classes()).not.toContain('is-fade-end')
    expect(source).toMatch(/\.table-toolbar__filters\.is-fade-start \{\s*-webkit-mask-image: linear-gradient\(to right, transparent, #000 24px\);/)
    wrapper.unmount()
  })
})

// Astra review: filters added after mounting widened the row with no fade until it was scrolled.
describe('TableToolbar filter row fade after filters change', () => {
  it('measures again when filters are added', async () => {
    // Sizes alone would not tell: the row keeps its width, so only the change of its filters can.
    vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
    const count = ref(2)
    const wrapper = mount(TableToolbar, {
      slots: { filters: () => Array.from({ length: count.value }, (_, i) => h('div', { class: 'f', key: i }, `f${i}`)) },
      attachTo: document.body
    })
    const row = wrapper.get('[data-test="toolbar-filters"]')
    const el = row.element as HTMLElement
    Object.defineProperty(el, 'clientWidth', { configurable: true, value: 390 })
    Object.defineProperty(el, 'scrollWidth', { configurable: true, get: () => el.children.length * 150 })
    await row.trigger('scroll')
    expect(row.classes()).not.toContain('is-fade-end')
    count.value = 6
    await nextTick()
    await new Promise((resolve) => setTimeout(resolve, 0))
    await nextTick()
    expect(row.classes()).toContain('is-fade-end')
    wrapper.unmount()
    vi.unstubAllGlobals()
  })
})

