import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../TablePageLayout.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('TablePageLayout responsive table scrolling', () => {
  it('fixes the page height only from 1024px, the top bar counted at its 65px: narrower pages scroll as a whole under the sticky top bar (7n-2, 7q-3)', () => {
    const fixed = Array.from(componentSource.matchAll(/@media \(min-width: 1024px\) \{\s*\.table-page-layout \{([^{}]*)\}/g))
    expect(fixed).toHaveLength(1)
    expect(fixed[0][1]).toContain('height: calc(100vh - 65px - 4rem)')
    expect(componentSource.match(/height: calc\(100vh/g)).toHaveLength(1)
  })

  it('does not disable the table horizontal scroll container in mobile mode', () => {
    const tableWrapperBlocks = Array.from(
      componentSource.matchAll(/([^{}]*:deep\(\.table-wrapper\)[^{}]*)\{([^{}]*)\}/g)
    )

    expect(tableWrapperBlocks.length).toBeGreaterThan(0)

    const baseBlock = tableWrapperBlocks.find(([selector]) => !selector.includes('.mobile-mode'))
    const mobileBlocks = tableWrapperBlocks.filter(([selector]) => selector.includes('.mobile-mode'))

    expect(baseBlock?.[2]).toContain('overflow-x-auto')
    expect(mobileBlocks.every(([, , declarations]) => !declarations.includes('overflow-visible'))).toBe(
      true
    )
  })
})
