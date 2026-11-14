import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createCommentVNode, h } from 'vue'

import TableToolbar from '../TableToolbar.vue'

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
    const column = narrow.match(/\.table-toolbar,\s*\.table-toolbar__main \{([^{}]*)\}/)![1]
    expect(column).toContain('flex-direction: column')
    expect(column).toContain('flex-wrap: nowrap')
  })

  it('makes the main part as tall as its rows: its wide basis would be about a height there (7q-2)', () => {
    expect(narrow).toMatch(/\n {2}\.table-toolbar__main \{\s*flex: none;\s*\}/)
  })
})
