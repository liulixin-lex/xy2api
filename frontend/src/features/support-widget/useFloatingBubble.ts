import { computed, onBeforeUnmount, onMounted, ref, type Ref } from 'vue'
import { useMotionPreference } from '@/composables/useMotionPreference'
import {
  BUBBLE_STORAGE_KEY,
  clampBubblePoint,
  parseBubblePosition,
  resolveBubblePoint,
  snapBubble,
  type BubbleBounds,
  type BubblePoint,
  type BubblePosition,
} from './snapBubble'

const DRAG_THRESHOLD_PX = 4
const EDGE_MARGIN_PX = 16
const HEADER_HEIGHT_PX = 64
export const BUBBLE_SIZE_PX = 60
export const BUBBLE_SIZE_SMALL_PX = 56
const SMALL_SCREEN_PX = 640

function readSafeAreas(): { top: number; bottom: number } {
  if (typeof document === 'undefined' || !document.body) return { top: 0, bottom: 0 }
  const probe = document.createElement('div')
  probe.style.cssText = 'position:fixed;visibility:hidden;pointer-events:none;padding-top:env(safe-area-inset-top);padding-bottom:env(safe-area-inset-bottom)'
  document.body.appendChild(probe)
  const style = getComputedStyle(probe)
  const areas = { top: parseFloat(style.paddingTop) || 0, bottom: parseFloat(style.paddingBottom) || 0 }
  probe.remove()
  return areas
}

function loadPosition(): BubblePosition {
  try {
    return parseBubblePosition(localStorage.getItem(BUBBLE_STORAGE_KEY))
  } catch {
    return parseBubblePosition(null)
  }
}

function savePosition(position: BubblePosition) {
  try {
    localStorage.setItem(BUBBLE_STORAGE_KEY, JSON.stringify(position))
  } catch {
    // Private mode: the position only lasts for this page.
  }
}

/**
 * Drag-and-snap behaviour for the support bubble, after the CustomPageView
 * open-button drag: pointer capture, a 4px threshold, and the click that ends a
 * drag is swallowed. The bubble snaps to the nearer side edge on pointerup,
 * pointercancel and lostpointercapture and is re-clamped on resize/rotation.
 */
export function useFloatingBubble(leftInset: Ref<number>) {
  const { reducedMotion } = useMotionPreference()
  const viewport = ref({ width: typeof window === 'undefined' ? 1024 : window.innerWidth, height: typeof window === 'undefined' ? 768 : window.innerHeight })
  const safeArea = ref({ top: 0, bottom: 0 })
  const position = ref<BubblePosition>(loadPosition())
  const dragPoint = ref<BubblePoint | null>(null)
  let drag: { pointerId: number; startX: number; startY: number; origin: BubblePoint; moved: boolean } | null = null
  let suppressClick = false

  const size = computed(() => (viewport.value.width < SMALL_SCREEN_PX ? BUBBLE_SIZE_SMALL_PX : BUBBLE_SIZE_PX))
  const bounds = computed<BubbleBounds>(() => ({
    viewportWidth: viewport.value.width,
    viewportHeight: viewport.value.height,
    size: size.value,
    margin: EDGE_MARGIN_PX,
    leftInset: leftInset.value,
    topInset: HEADER_HEIGHT_PX + safeArea.value.top,
    bottomInset: safeArea.value.bottom,
  }))
  const point = computed(() => dragPoint.value ?? resolveBubblePoint(position.value, bounds.value))
  const dragging = computed(() => dragPoint.value !== null)
  const style = computed(() => ({
    width: `${size.value}px`,
    height: `${size.value}px`,
    transform: `translate3d(${Math.round(point.value.x)}px, ${Math.round(point.value.y)}px, 0)`,
    transition: dragging.value || reducedMotion.value ? 'none' : 'transform 200ms cubic-bezier(0.2, 0, 0, 1)',
  }))

  function onPointerDown(event: PointerEvent) {
    if (event.button !== 0 || !event.isPrimary) return
    suppressClick = false
    drag = { pointerId: event.pointerId, startX: event.clientX, startY: event.clientY, origin: { ...point.value }, moved: false }
    const target = event.currentTarget as HTMLElement | null
    target?.setPointerCapture?.(event.pointerId)
  }

  function onPointerMove(event: PointerEvent) {
    if (!drag || event.pointerId !== drag.pointerId) return
    const dx = event.clientX - drag.startX
    const dy = event.clientY - drag.startY
    if (!drag.moved && Math.hypot(dx, dy) < DRAG_THRESHOLD_PX) return
    drag.moved = true
    suppressClick = true
    dragPoint.value = clampBubblePoint({ x: drag.origin.x + dx, y: drag.origin.y + dy }, bounds.value)
    event.preventDefault()
  }

  function onPointerEnd(event: PointerEvent) {
    if (!drag || event.pointerId !== drag.pointerId) return
    const moved = drag.moved
    drag = null
    const target = event.currentTarget as HTMLElement | null
    if (target?.hasPointerCapture?.(event.pointerId)) target.releasePointerCapture(event.pointerId)
    if (!moved || !dragPoint.value) return
    position.value = snapBubble(dragPoint.value, bounds.value)
    dragPoint.value = null
    savePosition(position.value)
  }

  /** Returns false for the click that ends a drag (keyboard clicks have detail 0 and always pass). */
  function acceptClick(event: MouseEvent): boolean {
    const swallow = suppressClick && event.detail !== 0
    suppressClick = false
    return !swallow
  }

  function onResize() {
    viewport.value = { width: window.innerWidth, height: window.innerHeight }
  }

  onMounted(() => {
    safeArea.value = readSafeAreas()
    onResize()
    window.addEventListener('resize', onResize)
    window.addEventListener('orientationchange', onResize)
  })

  onBeforeUnmount(() => {
    window.removeEventListener('resize', onResize)
    window.removeEventListener('orientationchange', onResize)
  })

  return {
    position,
    point,
    size,
    bounds,
    dragging,
    style,
    viewport,
    acceptClick,
    handlers: {
      pointerdown: onPointerDown,
      pointermove: onPointerMove,
      pointerup: onPointerEnd,
      pointercancel: onPointerEnd,
      lostpointercapture: onPointerEnd,
    },
  }
}
