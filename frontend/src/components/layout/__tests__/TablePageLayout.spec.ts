import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, inject, nextTick } from 'vue'

import TablePageLayout from '../TablePageLayout.vue'
import { TABLE_REGION_KEY } from '../tableRegion'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../TablePageLayout.vue')
const componentSource = readFileSync(componentPath, 'utf8')

describe('TablePageLayout responsive table scrolling', () => {
  it('fixes a card page\'s height from 1024px, the top bar counted at its 65px, so it scrolls as a whole below (7n-2, 7q-3); a table-layout page only in its region', () => {
    const fixed = Array.from(componentSource.matchAll(/@media \(min-width: 1024px\) \{\s*\.table-page-layout:not\(\.table-page-layout--table\) \{([^{}]*)\}/g))
    expect(fixed).toHaveLength(1)
    expect(fixed[0][1]).toContain('height: calc(100vh - 65px - 4rem)')
    const heights = Array.from(componentSource.matchAll(/\n *(\S[^{}\n]*) \{\s*height: calc\(100vh[^;]*;/g)).map((match) => match[1])
    expect(heights).toEqual(['.table-page-layout:not(.table-page-layout--table)', '.table-page-layout--region', '.table-page-layout--region', '.table-page-layout--region'])
  })

  it('sizes the table region to the visible height at every width: dvh after the vh fallback, less the top bar and main\'s padding', () => {
    expect(componentSource).toMatch(/\n\.table-page-layout--region \{\s*height: calc\(100vh - 65px - 2rem\);\s*height: calc\(100dvh - 65px - 2rem\);/)
    expect(componentSource).toMatch(/@media \(min-width: 768px\) \{\s*\.table-page-layout--region \{\s*height: calc\(100vh - 65px - 3rem\);\s*height: calc\(100dvh - 65px - 3rem\);/)
    expect(componentSource).toMatch(/@media \(min-width: 1024px\) \{\s*\.table-page-layout--region \{\s*height: calc\(100vh - 65px - 4rem\);\s*height: calc\(100dvh - 65px - 4rem\);/)
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

// Mobile list plan: below 1024px the table layout fills the visible height and only its table region scrolls; under
// 500px of visible height (a phone on its side), or when the page asks for it, the whole page scrolls instead.
describe('TablePageLayout table layout: one decision for region or page scroll (useTableRegion)', () => {
  const regions: boolean[] = []
  const Probe = defineComponent({
    setup() {
      const region = inject(TABLE_REGION_KEY)!
      return () => { regions.push(region.value); return h('div') }
    }
  })
  const height = window.innerHeight
  beforeEach(() => {
    regions.length = 0
    window.matchMedia = ((query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} })) as unknown as typeof window.matchMedia
  })
  const width = window.innerWidth
  afterEach(() => {
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: height })
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: width })
  })
  const mountLayout = (props: Record<string, unknown>) => mount(TablePageLayout, { props, slots: { table: () => h(Probe) } })

  it('bounds the table region on a tall screen and tells the table so', () => {
    const wrapper = mountLayout({ tableLayout: true })
    expect(wrapper.classes()).toEqual(expect.arrayContaining(['table-page-layout--table', 'table-page-layout--region']))
    expect(wrapper.classes()).not.toContain('mobile-mode')
    expect(regions.at(-1)).toBe(true)
  })

  it('lets the whole page scroll under 500px of visible height, or when the page asks', () => {
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 420 })
    const short = mountLayout({ tableLayout: true })
    expect(short.classes()).toContain('table-page-layout--table')
    expect(short.classes()).not.toContain('table-page-layout--region')
    expect(regions.at(-1)).toBe(false)
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 844 })
    const asked = mountLayout({ tableLayout: true, pageScroll: true })
    expect(asked.classes()).not.toContain('table-page-layout--region')
    expect(regions.at(-1)).toBe(false)
  })

  it('keeps the old narrow layout for card pages', () => {
    const wrapper = mountLayout({})
    expect(wrapper.classes()).toContain('mobile-mode')
    expect(regions.at(-1)).toBe(false)
  })

  // Astra review #1: a bulk bar and wrapped tags left 8–28px of table. The room is everything but the table's wrapper,
  // measured the same in either mode: no header and one whole row in it, the page scrolls.
  it('scrolls the page when the room left for the table cannot hold its header and one row, and goes back once it can', async () => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 390 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 568 })
    const sizes = { root: 471, wrapper: 300 }
    const Table = defineComponent({
      setup() {
        inject(TABLE_REGION_KEY)
        return () => h('div', { class: 'table-wrapper' }, [h('table', [h('thead'), h('tbody', [h('tr')])])])
      }
    })
    const wrapper = mount(TablePageLayout, { props: { tableLayout: true }, slots: { table: () => h(Table) }, attachTo: document.body })
    const el = wrapper.element as HTMLElement
    const box = el.querySelector('.table-wrapper') as HTMLElement
    Object.defineProperty(el, 'offsetHeight', { configurable: true, get: () => sizes.root })
    Object.defineProperty(box, 'offsetHeight', { configurable: true, get: () => sizes.wrapper })
    Object.defineProperty(box.querySelector('thead')!, 'offsetHeight', { configurable: true, get: () => 45 })
    Object.defineProperty(box.querySelector('tbody tr')!, 'offsetHeight', { configurable: true, get: () => 78 })
    // 568 − 65 − 32 = 471 for the page; the others take 471 − 300 = 171, leaving 300 ≥ 45 + 78.
    window.dispatchEvent(new Event('resize'))
    await nextTick()
    expect(wrapper.classes()).toContain('table-page-layout--region')
    // A bulk bar comes: the others take 471 − 60 = 411, leaving 60.
    sizes.wrapper = 60
    window.dispatchEvent(new Event('resize'))
    await nextTick()
    expect(wrapper.classes()).not.toContain('table-page-layout--region')
    // Page scroll: the wrapper takes its rows' height and the page grows by as much; the others are still 411.
    sizes.wrapper = 1600
    sizes.root = 2011
    window.dispatchEvent(new Event('resize'))
    await nextTick()
    expect(wrapper.classes()).not.toContain('table-page-layout--region')
    // The bar goes: 1600 + 171.
    sizes.root = 1771
    window.dispatchEvent(new Event('resize'))
    await nextTick()
    expect(wrapper.classes()).toContain('table-page-layout--region')
    wrapper.unmount()
  })

  // Astra review #8: the short-screen rule holds at every width (1024×480 scrolled inside a fixed page).
  it('scrolls the page under 500px of visible height on a desktop width too', () => {
    window.matchMedia = ((query: string) => ({ matches: true, media: query, addEventListener: () => {}, removeEventListener: () => {} })) as unknown as typeof window.matchMedia
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 1024 })
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 480 })
    const wrapper = mountLayout({ tableLayout: true })
    expect(wrapper.classes()).not.toContain('table-page-layout--region')
    expect(regions.at(-1)).toBe(false)
    // A card page keeps its desktop rule.
    expect(mountLayout({}).classes()).not.toContain('table-page-layout--table')
    expect(regions.at(-1)).toBe(true)
  })
})
