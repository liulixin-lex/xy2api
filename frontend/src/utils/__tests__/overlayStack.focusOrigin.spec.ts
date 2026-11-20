import { afterEach, describe, expect, it } from 'vitest'

import { closeOverlay, focusOrigin, openOverlay } from '../overlayStack'

// focusOrigin (Astra re-review, cross-breakpoint focus): a toolbar about to remove its sheet asks which of its elements
// the focus belongs to; focus in a Select's list, teleported out of the sheet, belongs to the Select's trigger.

const tokens: symbol[] = []
afterEach(() => {
  tokens.splice(0).reverse().forEach((token) => closeOverlay(token))
  document.body.innerHTML = ''
})
const el = <K extends keyof HTMLElementTagNameMap>(tag: K, parent: Element = document.body, attrs: Record<string, string> = {}) => {
  const node = document.createElement(tag)
  Object.entries(attrs).forEach(([name, value]) => node.setAttribute(name, value))
  parent.appendChild(node)
  return node
}
/** A sub-layer (a Select's list) opened from whatever has the focus, teleported to the body. */
const openList = () => {
  const list = el('div')
  const search = el('input', list)
  const token = Symbol('list')
  tokens.push(token)
  openOverlay(token, { root: () => list, panel: () => list, modal: false })
  return { list, search }
}

describe('focusOrigin', () => {
  it('is the focused element when it lies in a region', () => {
    const sheet = el('div')
    const field = el('input', sheet)
    field.focus()
    expect(focusOrigin([sheet])).toBe(field)
  })

  it("is a list's opener when the focus is in a list opened from the region", () => {
    const sheet = el('div')
    const trigger = el('button', sheet, { 'aria-expanded': 'true' })
    trigger.focus()
    const { search } = openList()
    search.focus()
    expect(focusOrigin([sheet])).toBe(trigger)
  })

  it('follows nested lists down to the region', () => {
    const sheet = el('div')
    const trigger = el('button', sheet)
    trigger.focus()
    const first = openList()
    const inner = el('button', first.list)
    inner.focus()
    const second = openList()
    second.search.focus()
    expect(focusOrigin([sheet])).toBe(trigger)
  })

  // Safari does not focus a pressed button: the list's opener is what held focus, here the sheet's panel.
  it('takes the open control inside an opener that is not itself the control (a press that did not focus it)', () => {
    const sheet = el('div')
    const panel = el('div', sheet, { tabindex: '-1' })
    el('button', panel, { 'aria-expanded': 'false', 'data-test': 'other' })
    const trigger = el('button', panel, { 'aria-expanded': 'true' })
    panel.focus()
    const { search } = openList()
    search.focus()
    expect(focusOrigin([sheet])).toBe(trigger)
  })

  it("falls back to the region's open control when the list's opener is elsewhere, the first region first", () => {
    const sheet = el('div')
    const toolbar = el('div')
    el('button', toolbar, { 'aria-expanded': 'true', 'data-test': 'filters' })
    const trigger = el('button', sheet, { 'aria-expanded': 'true' })
    document.body.focus()
    const { search } = openList()
    search.focus()
    expect(focusOrigin([sheet, toolbar])).toBe(trigger)
  })

  it('is null when the focus is elsewhere on the page', () => {
    const sheet = el('div')
    el('button', sheet, { 'aria-expanded': 'true' })
    const outside = el('button')
    outside.focus()
    expect(focusOrigin([sheet])).toBeNull()
    expect(focusOrigin([null, undefined])).toBeNull()
  })
})
