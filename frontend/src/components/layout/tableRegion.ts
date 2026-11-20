import { computed, onBeforeUnmount, onMounted, ref, watch, type InjectionKey, type Ref } from 'vue'

/**
 * Provided by TablePageLayout to the DataTable inside it: whether the table's own wrapper is a bounded area that scrolls
 * both ways (the fixed-height table region), so rows can be windowed there. False while the whole page scrolls. A
 * table in the table layout outside TablePageLayout gets nothing and counts as page scroll at every width.
 */
export const TABLE_REGION_KEY: InjectionKey<Readonly<Ref<boolean>>> = Symbol('table-region')

/** Below this visible height (a phone on its side, a small phone with the keyboard up) the whole page scrolls. */
export const SHORT_VIEWPORT_PX = 500
/** The page header's height (64px and its 1px border), as TablePageLayout's fixed height counts it. */
const HEADER_PX = 65
/** AppLayout's main padding, top and bottom together: p-4, md:p-6, lg:p-8. */
const mainPadding = (width: number) => (width >= 1024 ? 64 : width >= 768 ? 48 : 32)
/** A table whose rows are not in yet still needs room for one. */
const ROW_FALLBACK_PX = 48

/**
 * The visible height in CSS pixels at the page's own scale: the visual viewport's height (a software keyboard shrinks
 * it) times its scale, so zooming in with two fingers does not count as a short screen.
 */
export const visibleHeight = () => {
  if (typeof window === 'undefined') return 1000
  const view = window.visualViewport
  return view ? view.height * (view.scale || 1) : window.innerHeight
}

/**
 * The table layout's one decision (mobile list plan): does the page fill the visible height with only its table
 * region scrolling (and windowing rows), or does the whole page scroll? At every width, the region is used only when
 * the page has not asked for page scroll, the visible height is at least SHORT_VIEWPORT_PX, and the room the region
 * would get, once the toolbar, tags, bulk bar, pagination and gaps took theirs, holds the table's header and one whole
 * row. That room is measured the same way in either mode (everything but the table's wrapper), so the answer does not
 * flip because it switched.
 */
export function useTableRegion(root: Ref<HTMLElement | null>, enabled: () => boolean) {
  const viewport = ref({ width: typeof window === 'undefined' ? 1440 : window.innerWidth, height: typeof window === 'undefined' ? 900 : window.innerHeight, visible: visibleHeight() })
  const fits = ref(true)

  const measure = () => {
    if (typeof window === 'undefined') return
    viewport.value = { width: window.innerWidth, height: window.innerHeight, visible: visibleHeight() }
    const el = root.value
    const wrapper = el?.querySelector<HTMLElement>('.table-wrapper')
    if (!el || !wrapper) {
      fits.value = true
      return
    }
    const target = viewport.value.height - HEADER_PX - mainPadding(viewport.value.width)
    const others = el.offsetHeight - wrapper.offsetHeight
    const head = wrapper.querySelector<HTMLElement>('thead')?.offsetHeight ?? 0
    const row = wrapper.querySelector<HTMLElement>('tbody tr')?.offsetHeight || ROW_FALLBACK_PX
    fits.value = target - others >= head + row
  }

  let observer: ResizeObserver | null = null
  const observed = new Set<Element>()
  const observe = () => {
    const el = root.value
    if (!observer || !el) return
    const targets = [el, ...Array.from(el.children), ...Array.from(el.querySelectorAll('.table-scroll-container > *, .table-wrapper, .table-wrapper > table'))]
    for (const target of targets) {
      if (observed.has(target)) continue
      observed.add(target)
      observer.observe(target)
    }
  }
  const onResize = () => {
    measure()
    observe()
  }
  onMounted(() => {
    if (typeof ResizeObserver !== 'undefined') observer = new ResizeObserver(onResize)
    window.addEventListener('resize', onResize)
    window.visualViewport?.addEventListener('resize', onResize)
    onResize()
  })
  onBeforeUnmount(() => {
    observer?.disconnect()
    observer = null
    window.removeEventListener('resize', onResize)
    window.visualViewport?.removeEventListener('resize', onResize)
  })
  // The table's wrapper can mount after the layout (a lazy page): look again once the root's content changes.
  watch(root, () => onResize(), { flush: 'post' })

  const short = computed(() => viewport.value.visible < SHORT_VIEWPORT_PX)
  const region = computed(() => enabled() && !short.value && fits.value)
  return { region, short, fits, measure: onResize }
}
