import { nextTick, watch, type Ref } from 'vue'

const FOCUSABLE = 'input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])'
const firstFocusable = (el: Element | null | undefined) =>
  (el ? ((el.matches(FOCUSABLE) ? el : el.querySelector(FOCUSABLE)) as HTMLElement | null) : null)
/** The sheets the phone toolbar opens (rendered in the body): filters, columns, "more". */
const SHEETS = '[data-test="filter-sheet"], [data-test="usage-column-sheet"], [data-test="usage-more-sheet"]'

/**
 * Keeps the focus when the usage filters change form at 1024px. These pages swap the shared phone toolbar for their
 * own inline row, so the toolbar is gone when the width crosses and its own hand-off cannot reach the row; this one
 * keeps the shared contract (data-filter): focus in the toolbar or one of its sheets moves to the row's field with the
 * same `data-filter` (the fields are defined once, so both copies carry the key), else the row's first field; focus in
 * the row moves to "Filters". Focus elsewhere on the page stays put.
 *
 * @param sheet  the toolbar form is on (below 1024px)
 * @param root   holds the toolbar and the row (the sheets are in the body)
 * @param row    the inline row, present from 1024px
 */
export function useFilterFocusHandoff(sheet: Ref<boolean>, root: Ref<HTMLElement | null>, row: Ref<HTMLElement | null>) {
  watch(sheet, async (on) => {
    const active = document.activeElement instanceof HTMLElement ? document.activeElement : null
    if (!active || active === document.body) return
    const inSheet = !!active.closest('[role="dialog"]')?.querySelector(SHEETS)
    const held = on ? !!row.value?.contains(active) : inSheet || !!root.value?.contains(active)
    if (!held) return
    const key = active.closest<HTMLElement>('[data-filter]')?.dataset.filter
    await nextTick()
    const target = on
      ? root.value?.querySelector<HTMLElement>('[data-test="toolbar-filter-button"]')
      : firstFocusable(key ? row.value?.querySelector(`[data-filter=${JSON.stringify(key)}]`) : null) ?? firstFocusable(row.value)
    target?.focus()
  }, { flush: 'pre' })
}
