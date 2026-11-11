// Open dialogs and drawers in opening order, each with its z-index. A newly opened layer is drawn above every
// open one, so what is on top on screen is also the layer that owns focus and Escape. The body stays
// scroll-locked while any layer is open.
const OVERLAY_BASE_Z = 50
const openOverlays = new Map<symbol, number>()

/** Registers a layer and returns its z-index: at least `floor`, and above every layer already open. */
export function openOverlay(token: symbol, floor = OVERLAY_BASE_Z): number {
  const current = openOverlays.get(token)
  if (current !== undefined) return current
  const zIndex = Math.max(floor, ...[...openOverlays.values()].map((z) => z + 1))
  openOverlays.set(token, zIndex)
  document.body.classList.add('modal-open')
  return zIndex
}

export function closeOverlay(token: symbol) {
  openOverlays.delete(token)
  if (openOverlays.size === 0) document.body.classList.remove('modal-open')
}

export function isTopOverlay(token: symbol) {
  return [...openOverlays.keys()].pop() === token
}
