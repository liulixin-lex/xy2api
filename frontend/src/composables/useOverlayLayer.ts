import { nextTick, onUnmounted, ref, toValue, watch, type Ref, type WatchSource } from 'vue'
import { closeOverlay, isTopOverlay, openOverlay, syncOverlays, trapTab } from '@/utils/overlayStack'

interface OverlayLayerOptions {
  /** Outermost element of the layer (made inert while a modal layer is above it). */
  root: Readonly<Ref<HTMLElement | null>>
  /** Element focused when nothing better is left in the layer; give it tabindex="-1". Defaults to `root`. */
  panel?: Readonly<Ref<HTMLElement | null>>
  /**
   * true (default): a modal layer. The page and every lower layer are inert while it is open and focus moves into
   * it. false: a sub-layer, i.e. a menu or popover opened from the page or from another layer (a Select menu in a
   * dialog). The layer it belongs to stays usable and keeps its focus rules, but while the sub-layer is open it is
   * the top layer: re-renders below do not pull focus out of it and Escape closes it first. It never moves focus by
   * itself.
   */
  modal?: boolean
  /** Lowest z-index (for a sub-layer: its own z-index). The layer is always drawn above those opened before it. */
  floor?: number
  /** Modal only: element that takes focus once the layer has rendered. Defaults to the panel. */
  initialFocus?: () => HTMLElement | null | undefined
  /** Escape while this layer is on top (after the layer's own key handlers, unless they prevented it). */
  onEscape?: () => void
  /** Sub-layer only: how to close it when a modal layer opens above it, without moving focus. Defaults to onEscape. */
  onCovered?: () => void
}

/**
 * Registers a hand-built layer (one that does not use BaseDialog or BaseDrawer) with the overlay stack (design
 * spec 0.2). A modal layer must be teleported out of #app, which is inert while any modal layer is open. When the
 * layer closes, focus goes back to the layer below unless the user has already moved it somewhere else.
 */
export function useOverlayLayer(show: WatchSource<boolean>, options: OverlayLayerOptions) {
  const token = Symbol(options.modal === false ? 'sub-layer' : 'modal')
  const modal = options.modal !== false
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
      zIndex.value = openOverlay(token, {
        floor: options.floor,
        modal,
        root: () => options.root.value,
        panel,
        // A sub-layer closes when a modal layer opens above it.
        onCovered: modal ? undefined : () => { if (toValue(show)) (options.onCovered ?? options.onEscape)?.() }
      })
      document.addEventListener('keydown', onKeydown)
      await nextTick()
      if (!isTopOverlay(token)) return
      syncOverlays()
      if (modal) (options.initialFocus?.() ?? panel())?.focus()
    },
    { immediate: true }
  )

  onUnmounted(detach)

  return {
    zIndex,
    /** Keeps Tab and Shift+Tab inside a modal layer; bind to keydown.tab on the root. */
    trapTab: (event: KeyboardEvent) => trapTab(event, panel())
  }
}
