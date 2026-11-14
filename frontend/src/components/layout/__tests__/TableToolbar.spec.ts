import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

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
