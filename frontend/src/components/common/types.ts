/**
 * Common component types
 */

export interface Column {
  key: string
  label: string
  sortable?: boolean
  class?: string
  /** Classes for the header cell only. */
  headerClass?: string
  /** Contiguous columns with the same group share a header cell above them (slot `header-group-<group>`). */
  group?: string
  /** aria-sort for a column whose sorting the parent manages. */
  ariaSort?: 'ascending' | 'descending' | 'none'
  formatter?: (value: any, row: any) => string
  /** Below 1024px: at the right of the card's head instead of among its fields (a short state such as a status). */
  cardHeadEnd?: boolean
  /** Below 1024px: a field across the whole card instead of one of its two columns (a bar that needs the width). */
  cardFullRow?: boolean
}
