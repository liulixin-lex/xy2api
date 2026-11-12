import { afterEach, describe, expect, it } from 'vitest'

import { closeOverlay, holdFocus, openOverlay, trapTab } from '../overlayStack'

// Layers are plain elements here; the components register theirs the same way.
function layer(name: string) {
  const root = document.createElement('div')
  root.dataset.layer = name
  const panel = document.createElement('section')
  panel.tabIndex = -1
  root.appendChild(panel)
  document.body.appendChild(root)
  const token = Symbol(name)
  return { root, panel, token, open: () => openOverlay(token, { root: () => root, panel: () => panel }), close: () => closeOverlay(token) }
}

function button(parent: HTMLElement, trigger: string) {
  const el = document.createElement('button')
  el.setAttribute('data-sq-trigger', trigger)
  parent.appendChild(el)
  return el
}

afterEach(() => {
  document.body.innerHTML = ''
  document.body.className = ''
})

describe('overlay focus rules', () => {
  it('returns to the opener, or to the element with the same data-* in the new top layer after a re-render', () => {
    const page = document.createElement('main')
    page.id = 'app'
    document.body.appendChild(page)
    const pageButton = button(page, 'num:1:ttfo')
    pageButton.focus()
    const drawer = layer('drawer')
    drawer.open()
    expect(page.inert).toBe(true)

    const link = button(drawer.panel, 'board:53')
    link.focus()
    const board = layer('board')
    board.open()
    expect(drawer.root.inert).toBe(true)

    // The drawer re-rendered its link while the board was on top.
    link.remove()
    const rerendered = button(drawer.panel, 'board:53')
    board.close()
    expect(drawer.root.inert).toBe(false)
    expect(document.activeElement).toBe(rerendered)

    drawer.close()
    expect(page.inert).toBe(false)
    expect(document.activeElement).toBe(pageButton)
  })

  it('skips hidden and inert copies and falls back to the top panel, then to the page opener', () => {
    const page = document.createElement('main')
    document.body.appendChild(page)
    const hiddenCopy = button(page, 'num:1:ttfo')
    hiddenCopy.hidden = true
    hiddenCopy.checkVisibility = () => false
    const visible = button(page, 'num:1:ttfo')
    visible.focus()
    const drawer = layer('drawer')
    drawer.open()
    const inner = button(drawer.panel, 'recent-toggle')
    inner.focus()
    const dialog = layer('dialog')
    dialog.open()

    // The opener inside the drawer is gone and nothing matches it: the drawer panel takes focus.
    inner.remove()
    dialog.close()
    expect(document.activeElement).toBe(drawer.panel)

    // The page opener was re-rendered; only its visible copy is a target.
    visible.remove()
    const fresh = button(page, 'num:1:ttfo')
    drawer.close()
    expect(document.activeElement).toBe(fresh)
  })

  it('keeps focus in the top layer across a re-render and falls back to its panel', () => {
    const drawer = layer('drawer')
    drawer.open()
    const clear = button(drawer.panel, 'auto:rate_limit')
    clear.focus()

    let restore = holdFocus(drawer.token)
    clear.remove()
    const again = button(drawer.panel, 'auto:rate_limit')
    restore()
    expect(document.activeElement).toBe(again)

    restore = holdFocus(drawer.token)
    again.remove()
    restore()
    expect(document.activeElement).toBe(drawer.panel)
    drawer.close()
  })

  it('moves Tab to the first and Shift+Tab to the last element when focus is on the panel itself', () => {
    const drawer = layer('drawer')
    drawer.open()
    const first = button(drawer.panel, 'a')
    const last = button(drawer.panel, 'b')
    drawer.panel.focus()
    trapTab(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, cancelable: true }), drawer.panel)
    expect(document.activeElement).toBe(last)
    drawer.panel.focus()
    trapTab(new KeyboardEvent('keydown', { key: 'Tab', cancelable: true }), drawer.panel)
    expect(document.activeElement).toBe(first)
    drawer.close()
  })
})
