<template>
  <div class="table-toolbar">
    <div class="table-toolbar__main">
      <div v-if="$slots.search" class="table-toolbar__search"><slot name="search" /></div>
      <div v-if="$slots.filters" class="table-toolbar__filters" data-test="toolbar-filters"><slot name="filters" /></div>
    </div>
    <div class="table-toolbar__actions">
      <div v-if="$slots.status" class="table-toolbar__status"><slot name="status" /></div>
      <slot name="actions" />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The toolbar of a table page, in TablePageLayout's #filters slot (spec 8.6, prototype):
 * - from 1024px one row: the search and the filters on the left (the filters wrap), then the live indicator (#status)
 *   and the actions on the right. When the actions do not fit beside the search, the whole action area moves to a row
 *   of its own, on the right, its buttons wrapping inside it: it never covers the search or leaves the screen (7o-2);
 * - below 1024px the search has a row of its own, the filters keep to one row that scrolls sideways out to the
 *   screen's edges (select menus open in the body, so the scroll does not clip them), and the last row holds the live
 *   indicator on the left and the actions on the right, the primary one included. No row is wider than the page (7n-1).
 *
 * A secondary action puts its text in `<span class="table-toolbar__label">`: below 1024px the text goes and the button
 * is a 40px square with its icon alone, so the button needs an aria-label and a tip (7o-3). The primary action
 * ("create", "add") does not use the class and keeps its text.
 */
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
/* Its basis is the search box: the actions share its row as long as they fit beside it, then the main part grows into
   the room left (the filters wrapping in it). */
.table-toolbar__main {
  display: flex;
  flex: 1 1 16rem;
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
  .table-toolbar :deep(:has(> .table-toolbar__label)) {
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
