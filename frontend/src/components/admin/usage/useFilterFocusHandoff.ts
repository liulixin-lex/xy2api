import { nextTick, watch, type Ref } from 'vue'
import { focusOrigin } from '@/utils/overlayStack'

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
 * the row moves to "Filters". Focus in a list opened from a field (a Select's, in the body) counts as on that field
 * (overlayStack's focusOrigin). Focus elsewhere on the page stays put.
 *
 * @param sheet  the toolbar form is on (below 1024px)
 * @param root   holds the toolbar and the row (the sheets are in the body)
 * @param row    the inline row, present from 1024px
 */
export function useFilterFocusHandoff(sheet: Ref<boolean>, root: Ref<HTMLElement | null>, row: Ref<HTMLElement | null>) {
  watch(sheet, async (on) => {
    // The sheets (dialogs in the body) before the toolbar: while the filter sheet is open "Filters" is expanded too.
    const sheets = [...document.querySelectorAll(SHEETS)].map((el) => el.closest('[role="dialog"]'))
    const regions = on ? [row.value] : [...sheets, root.value]
    const origin = focusOrigin(regions)
    if (!origin) return
    // A tap does not focus a button in Safari: a list opened from a field may name the sheet itself as its opener. The
    // field whose list is open (aria-expanded) gives the key then.
    const expanded = regions.map((region) => region?.querySelector('[data-filter] [aria-expanded="true"]')).find(Boolean)
    const key = (origin.closest<HTMLElement>('[data-filter]') ?? expanded?.closest<HTMLElement>('[data-filter]'))?.dataset.filter
    await nextTick()
    const target = on
      ? root.value?.querySelector<HTMLElement>('[data-test="toolbar-filter-button"]')
      : firstFocusable(key ? row.value?.querySelector(`[data-filter=${JSON.stringify(key)}]`) : null) ?? firstFocusable(row.value)
    target?.focus()
  }, { flush: 'pre' })
}
