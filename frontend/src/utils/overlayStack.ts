// Open dialogs, drawers and popovers, in opening order (design spec 0.2).
// - A newly opened (or reused) layer is drawn above every open one, so the layer on top on screen is the one that
//   owns focus and Escape.
// - Below the topmost modal layer, the page (#app) and every lower layer are inert; the body is scroll-locked while
//   a modal layer is open. Non-modal layers (popovers) neither lock nor make anything inert.
// - When the top layer closes, focus goes to: its opener if still usable in the new top layer (or in the page when
//   no layer is left); else the element with the same data-* attributes there; else the new top layer's panel;
//   with no layer left, the element that opened the first layer from the page.
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

/** Usable focus target: connected, visible, not inert, and inside `scope` (null = the page outside any layer). */
function usable(el: HTMLElement | null, scope: HTMLElement | null): el is HTMLElement {
  if (!el || !el.isConnected || el.closest('[inert]') || !visible(el)) return false
  return scope ? scope.contains(el) : !insideLayer(el)
}

function findByData(data: Record<string, string>, scope: HTMLElement | null): HTMLElement | null {
  const entries = Object.entries(data)
  if (!entries.length) return null
  const selector = entries.map(([name, value]) => `[${name}="${value.replace(/["\\]/g, '\\$&')}"]`).join('')
  const found = Array.from((scope ?? document).querySelectorAll<HTMLElement>(selector))
  return found.find((el) => usable(el, scope)) ?? null
}

function restoreFocus(closed: Layer) {
  const top = layers[layers.length - 1]
  const scope = top ? top.root() : null
  const target =
    (usable(closed.opener.el, scope) ? closed.opener.el : null) ??
    findByData(closed.opener.data, scope) ??
    (top
      ? top.panel()
      : (pageOpener && (usable(pageOpener.el, null) ? pageOpener.el : findByData(pageOpener.data, null))) ?? null)
  target?.focus()
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
  const opener = snapshot(document.activeElement)
  const index = layers.findIndex((layer) => layer.token === token)
  if (index !== -1) layers.splice(index, 1)
  if (!layers.length) pageOpener = opener
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
  if (index === layers.length) restoreFocus(closed)
  if (!layers.length) pageOpener = null
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
