// Open dialogs, drawers and popovers, in opening order (design spec 0.2).
// - A newly opened (or reused) layer is drawn above every open one, so the layer on top on screen is the one that
//   owns focus and Escape.
// - Below the topmost modal layer, the page (#app) and every lower layer are inert; the body is scroll-locked while
//   a modal layer is open. Non-modal layers (popovers) neither lock nor make anything inert.
// - When the top layer closes, focus goes to: its opener if still usable in the new top layer (or in the page when
//   no layer is left); else the element with the same data-* attributes there; else the new top layer's panel;
//   with no layer left, the element that opened the first layer from the page. A layer reused from inside itself
//   keeps its earlier opener. If a re-render right after the close drops focus to the body, it goes to the target's
//   new copy (see settleFocus).
const OVERLAY_BASE_Z = 50

interface FocusRef {
  el: HTMLElement | null
  data: Record<string, string>
}

interface Layer {
  token: symbol
  z: number
  modal: boolean
  root: () => HTMLElement | null
  panel: () => HTMLElement | null
  opener: FocusRef
}

export interface OverlayOptions {
  /** Lowest z-index for this layer. */
  floor?: number
  /** Modal layers make the page and lower layers inert and lock scrolling. */
  modal?: boolean
  /** Outermost element of the layer (gets `inert` while a modal layer is above it). */
  root: () => HTMLElement | null
  /** Element that takes focus when nothing better is left in this layer. */
  panel: () => HTMLElement | null
}

const layers: Layer[] = []
let pageOpener: FocusRef | null = null

// data-* attributes identify a re-rendered opener; Vue's scoped-style attributes (data-v-*) are not identifying.
const snapshot = (el: Element | null): FocusRef => {
  const element = el instanceof HTMLElement && el !== document.body ? el : null
  const data: Record<string, string> = {}
  for (const attr of element?.attributes ?? []) {
    if (attr.name.startsWith('data-') && !attr.name.startsWith('data-v-')) data[attr.name] = attr.value
  }
  return { el: element, data }
}

const visible = (el: HTMLElement) => el.checkVisibility?.({ checkVisibilityCSS: true }) ?? true

const insideLayer = (el: Element) => layers.some((layer) => layer.root()?.contains(el))

/** The active page: #app (teleported tips, menus and toasts outside it are no return targets). */
const pageRoot = () => document.getElementById('app')

/**
 * Usable focus target: connected, enabled, visible, not inert, inside `scope` (null = the page outside any layer)
 * and not inside `leaving` (a layer that has just closed and may still be fading out).
 */
function usable(el: HTMLElement | null, scope: HTMLElement | null, leaving?: HTMLElement | null): el is HTMLElement {
  if (!el || !el.isConnected || el.matches(':disabled') || el.closest('[inert]') || !visible(el)) return false
  if (leaving?.contains(el)) return false
  if (scope) return scope.contains(el)
  const page = pageRoot()
  return (!page || page.contains(el)) && !insideLayer(el)
}

function findByData(data: Record<string, string>, scope: HTMLElement | null, leaving?: HTMLElement | null): HTMLElement | null {
  const entries = Object.entries(data)
  if (!entries.length) return null
  const selector = entries.map(([name, value]) => `[${name}="${value.replace(/["\\]/g, '\\$&')}"]`).join('')
  const found = Array.from((scope ?? pageRoot() ?? document).querySelectorAll<HTMLElement>(selector))
  return found.find((el) => usable(el, scope, leaving)) ?? null
}

/** The element itself when still usable, else its re-rendered copy (same data-*) in `scope`. */
const resolve = (ref: FocusRef, scope: HTMLElement | null, leaving?: HTMLElement | null) =>
  (usable(ref.el, scope, leaving) ? ref.el : null) ?? findByData(ref.data, scope, leaving)

/** Where focus goes once `closed` has left the stack; null while nothing is usable. */
function focusTarget(closed: Layer, page: FocusRef | null): HTMLElement | null {
  const top = layers[layers.length - 1]
  const leaving = closed.root()
  if (top) return resolve(closed.opener, top.root(), leaving) ?? top.panel()
  return resolve(closed.opener, null, leaving) ?? (page ? resolve(page, null, leaving) : null)
}

// A close often triggers a re-render of what lies below (the list refreshes once the drawer is gone), which can
// replace the element focus just went to or bring it back only later. Until the next pointer or key input, a new
// layer, or 1 s, focus that has fallen to the body goes to the target's current copy.
const SETTLE_MS = 1000
let stopSettling: (() => void) | null = null

function settleFocus(closed: Layer, page: FocusRef | null) {
  stopSettling?.()
  if (typeof MutationObserver === 'undefined') return
  const top = layers[layers.length - 1]
  const retry = () => {
    if (layers[layers.length - 1] !== top) return stop()
    const active = document.activeElement
    if (active && active !== document.body && active.isConnected) return
    focusTarget(closed, page)?.focus()
  }
  const observer = new MutationObserver(retry)
  const timer = setTimeout(() => stop(), SETTLE_MS)
  function stop() {
    observer.disconnect()
    clearTimeout(timer)
    window.removeEventListener('pointerdown', stop, true)
    window.removeEventListener('keydown', stop, true)
    if (stopSettling === stop) stopSettling = null
  }
  observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['hidden', 'inert', 'class', 'style'] })
  window.addEventListener('pointerdown', stop, true)
  window.addEventListener('keydown', stop, true)
  stopSettling = stop
}

/** Re-applies inert and the scroll lock; call after a layer's root has rendered. */
export function syncOverlays() {
  if (typeof document === 'undefined') return
  let topModal = -1
  layers.forEach((layer, index) => { if (layer.modal) topModal = index })
  layers.forEach((layer, index) => {
    const root = layer.root()
    if (root) root.inert = index < topModal
  })
  const app = document.getElementById('app')
  if (app) app.inert = topModal >= 0
  document.body.classList.toggle('modal-open', topModal >= 0)
}

/**
 * Registers a layer, or brings an open one to the top with the focused element as its new opener.
 * Returns its z-index: at least `floor` and above every other open layer.
 */
export function openOverlay(token: symbol, options: OverlayOptions): number {
  stopSettling?.()
  const focused = snapshot(document.activeElement)
  const index = layers.findIndex((layer) => layer.token === token)
  const previous = index === -1 ? null : layers.splice(index, 1)[0]
  // Reused from inside itself (a drawer switching to the parent account): an element of the layer is no place to
  // return to once it closes, so the earlier opener stays. Only a first opening sets the page opener.
  const opener = previous && focused.el && previous.root()?.contains(focused.el) ? previous.opener : focused
  if (!previous && !layers.length) pageOpener = focused
  const z = Math.max(options.floor ?? OVERLAY_BASE_Z, ...layers.map((layer) => layer.z + 1))
  layers.push({ token, z, modal: options.modal ?? true, root: options.root, panel: options.panel, opener })
  syncOverlays()
  return z
}

/** Removes a layer; when it was the top layer, focus moves as described above. */
export function closeOverlay(token: symbol) {
  const index = layers.findIndex((layer) => layer.token === token)
  if (index === -1) return
  const [closed] = layers.splice(index, 1)
  const root = closed.root()
  if (root) root.inert = false
  syncOverlays()
  const page = pageOpener
  if (!layers.length) pageOpener = null
  if (index !== layers.length) return
  focusTarget(closed, page)?.focus()
  settleFocus(closed, page)
}

/**
 * Call before a layer's content re-renders; the returned function, called after the render, puts focus back when
 * the update removed the focused element: the same element if still usable, else the one with the same data-*
 * attributes, else the layer's panel. Does nothing unless the layer is on top.
 */
export function holdFocus(token: symbol): () => void {
  const layer = layers[layers.length - 1]
  if (layer?.token !== token) return () => {}
  const before = snapshot(document.activeElement)
  return () => {
    const root = layer.root()
    if (layers[layers.length - 1] !== layer || !root) return
    const active = document.activeElement as HTMLElement | null
    if (active && active !== document.body && usable(active, root)) return
    ;((usable(before.el, root) ? before.el : null) ?? findByData(before.data, root) ?? layer.panel())?.focus()
  }
}

export function isTopOverlay(token: symbol) {
  return layers[layers.length - 1]?.token === token
}

/**
 * Tab and Shift+Tab stay inside `panel`. When focus is not on one of its focusable elements (for example on the
 * panel itself right after opening), Tab goes to the first and Shift+Tab to the last.
 */
export function trapTab(event: KeyboardEvent, panel: HTMLElement | null) {
  if (!panel) return
  const elements = focusableIn(panel)
  const first = elements[0]
  const last = elements[elements.length - 1]
  const active = document.activeElement as HTMLElement | null
  if (!first) {
    event.preventDefault()
    panel.focus()
    return
  }
  const at = active ? elements.indexOf(active) : -1
  if (at === -1) {
    event.preventDefault()
    ;(event.shiftKey ? last : first).focus()
  } else if (event.shiftKey && at === 0) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && at === elements.length - 1) {
    event.preventDefault()
    first.focus()
  }
}

export function focusableIn(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>('button, [href], input, select, textarea, summary, [tabindex]'))
    .filter((element) => !element.matches(':disabled, [tabindex="-1"]') &&
      !element.closest('[hidden], [inert], [aria-hidden="true"]') &&
      visible(element))
}
