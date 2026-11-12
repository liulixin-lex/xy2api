import { nextTick, onUnmounted, ref, watch, type Ref, type WatchSource } from 'vue'
import { closeOverlay, isTopOverlay, openOverlay, syncOverlays, trapTab } from '@/utils/overlayStack'

interface ModalLayerOptions {
  /** Outermost element of the modal (made inert while a modal layer is above it). */
  root: Ref<HTMLElement | null>
  /** Element focused when nothing better is left in the layer; give it tabindex="-1". Defaults to `root`. */
  panel?: Ref<HTMLElement | null>
  /** Lowest z-index; the layer is always drawn above the layers opened before it. */
  floor?: number
  /** Element that takes focus once the modal has rendered. Defaults to the panel. */
  initialFocus?: () => HTMLElement | null | undefined
  /** Escape while this modal is the top layer. */
  onEscape?: () => void
}

/**
 * Registers a hand-built modal (one that does not use BaseDialog or BaseDrawer) as a layer of the overlay stack
 * (design spec 0.2): it is drawn above every open layer, the page and the lower layers are inert while it is open,
 * it alone answers Escape while on top, and focus goes back to the layer below when it closes. The modal must be
 * teleported out of #app, which is inert while any modal layer is open.
 */
export function useModalLayer(show: WatchSource<boolean>, options: ModalLayerOptions) {
  const token = Symbol('modal')
  const zIndex = ref<number>()
  const panel = () => options.panel?.value ?? options.root.value

  function onKeydown(event: KeyboardEvent) {
    if (event.key !== 'Escape' || event.defaultPrevented || !isTopOverlay(token)) return
    event.preventDefault()
    options.onEscape?.()
  }

  function detach() {
    document.removeEventListener('keydown', onKeydown)
    closeOverlay(token)
  }

  watch(
    show,
    async (open) => {
      if (!open) return detach()
      zIndex.value = openOverlay(token, { floor: options.floor, root: () => options.root.value, panel })
      document.addEventListener('keydown', onKeydown)
      await nextTick()
      if (!isTopOverlay(token)) return
      syncOverlays()
      ;(options.initialFocus?.() ?? panel())?.focus()
    },
    { immediate: true }
  )

  onUnmounted(detach)

  return {
    zIndex,
    /** Keeps Tab and Shift+Tab inside the modal; bind to keydown.tab on the root. */
    trapTab: (event: KeyboardEvent) => trapTab(event, panel())
  }
}
