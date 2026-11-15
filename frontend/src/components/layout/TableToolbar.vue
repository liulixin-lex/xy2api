<template>
  <div class="table-toolbar" :class="{ 'table-toolbar--compact': compact }">
    <div class="table-toolbar__main">
      <div v-if="$slots.search" class="table-toolbar__search"><slot name="search" /></div>
      <div v-if="filled($slots.filters)" class="table-toolbar__filters" data-test="toolbar-filters"><slot name="filters" /></div>
    </div>
    <div class="table-toolbar__actions">
      <div v-if="$slots.status" class="table-toolbar__status"><slot name="status" /></div>
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { Comment, Fragment, Text, type Slot, type VNode } from 'vue'

/**
 * The toolbar of a table page, in TablePageLayout's #filters slot (spec 8.6, prototype):
 * - from 1024px one row: the search and the filters on the left, then the live indicator (#status) and the actions on
 *   the right. When the actions do not fit beside the search and all the filters, the whole action area moves to a
 *   row of its own, on the right, its buttons wrapping inside it: it never covers the search or leaves the screen, and
 *   the filters stay beside the search as long as they fit there, wrapping only as a group (7o-2, 7r-5);
 * - below 1024px the search has a row of its own, the filters keep to one row that scrolls sideways out to the
 *   screen's edges (select menus open in the body, so the scroll does not clip them), and the last row holds the live
 *   indicator on the left and the actions on the right, the primary one included. No row is wider than the page (7n-1).
 *
 * A secondary action puts its text in `<span class="table-toolbar__label">`: below 1024px the text goes and the button
 * is a 40px square with its icon alone, so the button needs an aria-label and a tip (7o-3). A button that is an icon
 * at every width (refresh) carries `table-toolbar__icon` and is the same square there. This is the only place the
 * square's size is set, for every page. The primary action ("create", "add") uses neither class and keeps its text.
 *
 * `compact` (spec 12.9), for the dense pages (accounts, groups, users): from 1024px the controls are 32px high in
 * 13px type and 8px apart, the search box 220px and a filter with `table-toolbar__filter` 112px. Their looks are set
 * here once for those pages:
 * - `table-toolbar__search-box` holding `table-toolbar__search-icon` and `table-toolbar__input` (a field without the
 *   icon adds `table-toolbar__input--plain`);
 * - `table-toolbar__btn`, outlined with `--secondary`, the brand gradient with `--primary` (12.1-5, the one filled
 *   button of the row);
 * - `table-toolbar__icon-btn`, a 32px outlined square for a tool (refresh, column settings), named by its tip;
 * - `table-toolbar__divider`, the 1px rule between the view tools and the page's actions.
 * A secondary action whose text sits in `table-toolbar__label table-toolbar__label--wide` shows it only while the
 * toolbar is at least 1184px wide, the widest compact row with every text (the groups page with its four filters);
 * narrower it is its icon in a 32px square, and the search box narrows to 192px, a filter to 104px: the groups page at
 * 1366 with the navigation open stays one row whatever the live indicator says (12.9-4). The accounts toolbar keeps
 * its own widths (its selects are not `table-toolbar__filter`). Below 1024px the narrow layout above applies, the
 * controls 40px high.
 */
defineProps<{ compact?: boolean }>()

/**
 * Whether a slot renders anything: a filters slot whose filters are all switched off (the users page) renders only
 * comments, and then takes no row and no gap (7s-3).
 */
const rendersSomething = (nodes: VNode[]): boolean => nodes.some((node) => {
  if (node.type === Comment) return false
  if (node.type === Text) return !!String(node.children).trim()
  if (node.type === Fragment) return Array.isArray(node.children) && rendersSomething(node.children as VNode[])
  return true
})
const filled = (slot: Slot | undefined) => !!slot && rendersSomething(slot())
</script>

<style scoped>
.table-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: flex-end;
  gap: 12px;
  min-width: 0;
}
/* Its basis is its content, the search and every filter in a row: the actions share that row when they fit beside it,
   else they take the next one, and the main part grows into the room left. A smaller basis kept the actions on the
   first row and squeezed the filters under the search, one above the other (7r-5). */
.table-toolbar__main {
  display: flex;
  flex: 1 1 auto;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.table-toolbar__search {
  flex: none;
  width: 16rem;
}
.table-toolbar__filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  min-width: 0;
}
.table-toolbar__filters > :deep(*) {
  flex: none;
}
.table-toolbar__actions {
  display: flex;
  flex: 0 1 auto;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  min-width: 0;
}
/* ---- compact (12.9): placed before the narrow rules, which win over it below 1024px ---- */
.table-toolbar--compact {
  container-type: inline-size;
}
@media (min-width: 1024px) {
  .table-toolbar--compact,
  .table-toolbar--compact .table-toolbar__main,
  .table-toolbar--compact .table-toolbar__filters {
    gap: 8px;
  }
  .table-toolbar--compact .table-toolbar__search {
    width: 220px;
  }
}
.table-toolbar--compact :deep(.table-toolbar__search-box) {
  position: relative;
  display: block;
}
.table-toolbar--compact :deep(.table-toolbar__filter) {
  width: 112px;
}
.table-toolbar--compact :deep(.table-toolbar__search-icon) {
  position: absolute;
  top: 50%;
  left: 10px;
  width: 14px;
  height: 14px;
  margin-top: -7px;
  color: var(--color-text-muted);
  pointer-events: none;
}
.table-toolbar--compact :deep(.table-toolbar__input) {
  width: 100%;
  height: var(--control-height);
  padding: 0 10px 0 30px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
}
.table-toolbar--compact :deep(.table-toolbar__input--plain) {
  padding-left: 10px;
}
.table-toolbar--compact :deep(.table-toolbar__input::placeholder) {
  color: var(--color-text-muted);
}
.table-toolbar--compact :deep(.table-toolbar__input:focus-visible) {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -1px;
  border-color: transparent;
}
.table-toolbar--compact :deep(.table-toolbar__btn) {
  display: inline-flex;
  height: var(--control-height);
  flex: none;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 0 12px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  font-size: var(--font-size-body);
  font-weight: var(--font-weight-medium);
  white-space: nowrap;
  transition: background-color var(--motion-fast) var(--ease-out), box-shadow var(--motion-fast) var(--ease-out);
}
.table-toolbar--compact :deep(.table-toolbar__btn--secondary) {
  border-color: var(--color-line-control);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
}
/* The brand gradient with a soft shadow of its own colour (12.1-5). */
.table-toolbar--compact :deep(.table-toolbar__btn--primary) {
  background: linear-gradient(to right, theme('colors.primary.500'), theme('colors.primary.600'));
  box-shadow: 0 2px 8px -2px theme('colors.primary.500 / 45%');
  color: #fff;
}
.table-toolbar--compact :deep(.table-toolbar__icon-btn) {
  display: inline-grid;
  width: var(--control-height);
  height: var(--control-height);
  flex: none;
  place-items: center;
  padding: 0;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: var(--color-bg-control);
  color: var(--color-text-secondary);
}
@media (hover: hover) {
  .table-toolbar--compact :deep(.table-toolbar__btn--secondary:not(:disabled):hover) {
    background: var(--color-bg-control-hover);
  }
  .table-toolbar--compact :deep(.table-toolbar__btn--primary:not(:disabled):hover) {
    background: linear-gradient(to right, theme('colors.primary.600'), theme('colors.primary.700'));
    box-shadow: 0 4px 12px -2px theme('colors.primary.500 / 50%');
  }
  .table-toolbar--compact :deep(.table-toolbar__icon-btn:not(:disabled):hover) {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.table-toolbar--compact :deep(.table-toolbar__btn:disabled),
.table-toolbar--compact :deep(.table-toolbar__icon-btn:disabled) {
  cursor: not-allowed;
  opacity: 0.5;
}
.table-toolbar--compact :deep(.table-toolbar__btn:focus-visible),
.table-toolbar--compact :deep(.table-toolbar__icon-btn:focus-visible) {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.table-toolbar--compact :deep(.table-toolbar__divider) {
  width: 1px;
  height: 16px;
  flex: none;
  background: var(--color-line-strong);
}
/* From 1024px, under the widest row with every text (1184px, see above) the secondary actions keep their icons and
   the search box and filters narrow (12.9-4); below 1024px the narrow layout makes them 40px squares. */
@media (min-width: 1024px) {
  @container (max-width: 1183px) {
    .table-toolbar--compact .table-toolbar__search {
      width: 192px;
    }
    .table-toolbar--compact :deep(.table-toolbar__filter) {
      width: 104px;
    }
    .table-toolbar--compact :deep(.table-toolbar__label--wide) {
      display: none;
    }
    .table-toolbar--compact :deep(.table-toolbar__btn:has(> .table-toolbar__label--wide)) {
      width: var(--control-height);
      padding: 0;
    }
  }
}
@media (max-width: 1023px) {
  /* One column that never wraps: a wrapping column takes the width of its widest row (the filters), which then widens
     the whole page (7n-1). */
  .table-toolbar,
  .table-toolbar__main {
    flex-direction: column;
    flex-wrap: nowrap;
    align-items: stretch;
    gap: 8px;
  }
  /* Its basis and growth are for the wide row; in the column they would be about a height (an empty band under the
     filters, 7q-2): here it is as tall as its rows. */
  .table-toolbar__main {
    flex: none;
  }
  .table-toolbar__search {
    width: 100%;
  }
  /* The filter row scrolls out to the screen's edges (prototype): it reaches across the page padding of AppLayout's
     main, where every page puts its toolbar, full width below 1024px (16px, 24px from 768px). */
  .table-toolbar__filters {
    flex-wrap: nowrap;
    gap: 8px;
    margin-inline: -16px;
    padding-inline: 16px;
    overflow-x: auto;
    scroll-padding-inline: 16px;
    scrollbar-width: none;
  }
  .table-toolbar__filters::-webkit-scrollbar {
    display: none;
  }
  .table-toolbar__status {
    margin-right: auto;
  }
  .table-toolbar :deep(.table-toolbar__label) {
    display: none;
  }
  .table-toolbar :deep(:has(> .table-toolbar__label)),
  .table-toolbar :deep(.table-toolbar__icon) {
    width: 40px;
    height: 40px;
    flex: none;
    justify-content: center;
    padding: 0;
  }
  .table-toolbar :deep(:has(> .table-toolbar__label) > *) {
    margin: 0;
  }
}
@media (min-width: 768px) and (max-width: 1023px) {
  .table-toolbar__filters {
    margin-inline: -24px;
    padding-inline: 24px;
    scroll-padding-inline: 24px;
  }
}
/* Compact below 1024px: the narrow layout's 40px controls; the divider goes (the row has no room for it). */
@media (max-width: 1023px) {
  .table-toolbar--compact :deep(.table-toolbar__input) {
    height: 40px;
    font-size: 15px;
  }
  .table-toolbar--compact :deep(.table-toolbar__btn) {
    height: 40px;
  }
  .table-toolbar--compact :deep(.table-toolbar__icon-btn) {
    width: 40px;
    height: 40px;
  }
  .table-toolbar--compact :deep(.table-toolbar__divider) {
    display: none;
  }
}
/* A mouse cannot swipe: it gets a thin scrollbar to reach the filters off screen; touch keeps it hidden (7j-6). */
@media (max-width: 1023px) and (pointer: fine) {
  .table-toolbar__filters {
    scrollbar-color: var(--color-line-control) transparent;
    scrollbar-width: thin;
  }
  .table-toolbar__filters::-webkit-scrollbar {
    display: block;
    height: 6px;
  }
  .table-toolbar__filters::-webkit-scrollbar-thumb {
    border-radius: 3px;
    background: var(--color-line-control);
  }
}
</style>
