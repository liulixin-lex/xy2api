import { computed, onScopeDispose, ref } from 'vue'
import { useMediaQuery } from '@vueuse/core'

/**
 * The widths and input a list page lays out by (mobile list plan): a phone below 768px, a tablet from 768 to 1023, a
 * desktop from 1024. Touch sizes follow the primary pointer (`pointer: coarse`, an iPad in landscape included), never
 * the width; a hybrid device with a touch screen besides its mouse matches `any-pointer: coarse` in CSS for its buttons.
 */
export const PHONE_QUERY = '(max-width: 767px)'
export const DESKTOP_QUERY = '(min-width: 1024px)'
export const COARSE_QUERY = '(pointer: coarse)'
/** Below this visible height (a phone on its side, or a small phone with the keyboard up) the whole page scrolls. */
export const SHORT_VIEWPORT_PX = 500

const visibleHeight = () => {
  if (typeof window === 'undefined') return 1000
  return window.visualViewport?.height ?? window.innerHeight
}

/**
 * Whether the visible part of the viewport is under SHORT_VIEWPORT_PX: the visual viewport when the browser has one, so
 * a software keyboard that leaves the layout viewport as it is still counts.
 */
export function useShortViewport() {
  const height = ref(visibleHeight())
  if (typeof window !== 'undefined') {
    const update = () => { height.value = visibleHeight() }
    const target: EventTarget = window.visualViewport ?? window
    target.addEventListener('resize', update)
    onScopeDispose(() => target.removeEventListener('resize', update))
  }
  return computed(() => height.value < SHORT_VIEWPORT_PX)
}

export function useTableViewport() {
  const phone = useMediaQuery(PHONE_QUERY)
  const desktop = useMediaQuery(DESKTOP_QUERY)
  const coarse = useMediaQuery(COARSE_QUERY)
  return { phone, desktop, coarse }
}
