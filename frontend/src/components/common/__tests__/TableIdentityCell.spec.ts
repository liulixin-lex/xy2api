import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import TableIdentityCell from '../TableIdentityCell.vue'

// A line cut by less than a pixel still offers its whole text on a tap: rounded widths alone miss it.

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.restoreAllMocks()
  delete (Range.prototype as { getBoundingClientRect?: unknown }).getBoundingClientRect
  document.getElementById('sq-tip')?.remove()
})
/** jsdom has no Range.getBoundingClientRect: each line's text width, by the element the range selects. */
const textWidths = (widths: Map<Node, number>) =>
  Object.defineProperty(Range.prototype, 'getBoundingClientRect', {
    configurable: true,
    value(this: Range) { return { width: widths.get(this.startContainer) ?? 0 } }
  })

const box = (el: Element, rounded: number, exact: number) => {
  Object.defineProperty(el, 'scrollWidth', { configurable: true, value: rounded })
  Object.defineProperty(el, 'clientWidth', { configurable: true, value: rounded })
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ width: exact } as DOMRect)
}

describe('TableIdentityCell', () => {
  it('shows the whole text on a tap when a line is cut by under a pixel', async () => {
    const wrapper = mount(TableIdentityCell, { props: { primary: 'someone@example.test', secondary: 'Someone' }, attachTo: document.body })
    const primary = wrapper.get('.table-identity__primary').element
    const secondary = wrapper.get('.table-identity__secondary').element
    box(primary, 47, 46.84)
    box(secondary, 30, 30)
    textWidths(new Map([[primary, 48.22], [secondary, 20]]))
    await wrapper.get('[data-test="table-identity"]').trigger('click')
    expect(document.getElementById('sq-tip')?.textContent).toBe('someone@example.test\nSomeone')
  })

  // Astra review: 116.484px of text in a 116.391px box, both 116 rounded, draws "…".
  it('shows the whole text when the rounded widths are equal but the text is cut', async () => {
    const wrapper = mount(TableIdentityCell, { props: { primary: 'tdf@example.test' }, attachTo: document.body })
    const primary = wrapper.get('.table-identity__primary').element
    box(primary, 116, 116.391)
    textWidths(new Map([[primary, 116.484]]))
    await wrapper.get('[data-test="table-identity"]').trigger('click')
    expect(document.getElementById('sq-tip')?.textContent).toBe('tdf@example.test')
  })

  it('offers no tip when both lines fit', async () => {
    const wrapper = mount(TableIdentityCell, { props: { primary: 'a@b.c', secondary: 'A' }, attachTo: document.body })
    const primary = wrapper.get('.table-identity__primary').element
    const secondary = wrapper.get('.table-identity__secondary').element
    box(primary, 40, 40)
    box(secondary, 20, 20)
    textWidths(new Map([[primary, 30], [secondary, 15]]))
    await wrapper.get('[data-test="table-identity"]').trigger('click')
    expect(document.getElementById('sq-tip')?.textContent ?? '').toBe('')
  })
})
