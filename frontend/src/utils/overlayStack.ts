// Open dialogs and drawers in opening order. The body stays scroll-locked while any is open,
// and Escape belongs to the one opened last (a dialog opened from a drawer closes first).
const openOverlays = new Set<symbol>()

export function openOverlay(token: symbol) {
  openOverlays.add(token)
  document.body.classList.add('modal-open')
}

export function closeOverlay(token: symbol) {
  openOverlays.delete(token)
  if (openOverlays.size === 0) document.body.classList.remove('modal-open')
}

export function isTopOverlay(token: symbol) {
  return [...openOverlays].pop() === token
}
