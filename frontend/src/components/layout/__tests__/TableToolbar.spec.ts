import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../TableToolbar.vue'), 'utf8')
const narrow = source.match(/@media \(max-width: 1023px\) \{([\s\S]*?)\n\}/)![1]

describe('TableToolbar below 1024px', () => {
  it('keeps the column from wrapping, so the filter row scrolls inside itself instead of widening the page (7n-1)', () => {
    const column = narrow.match(/\.table-toolbar,\s*\.table-toolbar__main \{([^{}]*)\}/)![1]
    expect(column).toContain('flex-direction: column')
    expect(column).toContain('flex-wrap: nowrap')
  })
})
