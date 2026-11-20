import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../TablePageLayout.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('TablePageLayout responsive table scrolling', () => {
  it('fixes the page height from 1024px, the top bar counted at its 65px; below 1024px only the table layout\'s region does, so card pages scroll as a whole under the sticky top bar (7n-2, 7q-3)', () => {
    const fixed = Array.from(componentSource.matchAll(/@media \(min-width: 1024px\) \{\s*\.table-page-layout \{([^{}]*)\}/g))
    expect(fixed).toHaveLength(1)
    expect(fixed[0][1]).toContain('height: calc(100vh - 65px - 4rem)')
    const heights = Array.from(componentSource.matchAll(/\n {2}(\S[^{}\n]*) \{\s*height: calc\(100vh[^;]*;/g)).map((match) => match[1])
    expect(heights).toEqual(['.table-page-layout', '.table-page-layout--region', '.table-page-layout--region'])
  })

  it('sizes the table region to the visible height below 1024px: dvh after the vh fallback, less the top bar and main\'s padding', () => {
    expect(componentSource).toMatch(/\.table-page-layout--region \{\s*height: calc\(100vh - 65px - 2rem\);\s*height: calc\(100dvh - 65px - 2rem\);/)
    expect(componentSource).toMatch(/@media \(min-width: 768px\) and \(max-width: 1023px\) \{\s*\.table-page-layout--region \{\s*height: calc\(100vh - 65px - 3rem\);\s*height: calc\(100dvh - 65px - 3rem\);/)
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
