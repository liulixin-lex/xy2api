import type { InjectionKey, Ref } from 'vue'

/**
 * Provided by TablePageLayout to the DataTable inside it: whether the table's own wrapper is a bounded area that scrolls
 * both ways (the fixed-height table region), so rows can be windowed there. False while the whole page scrolls (a
 * page that asks for it, a short viewport). A table outside TablePageLayout gets nothing and counts as page scroll
 * below 1024px.
 */
export const TABLE_REGION_KEY: InjectionKey<Readonly<Ref<boolean>>> = Symbol('table-region')
