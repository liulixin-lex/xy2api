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
 *   and the actions on the right;
 * - below 1024px the search has a row of its own, the filters keep to one row that scrolls sideways out to the
 *   screen's edges (select menus open in the body, so the scroll does not clip them), and the last row holds the live indicator on the left and the
 *   actions on the right, the primary one included.
 */
</script>

<style scoped>
.table-toolbar {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  min-width: 0;
}
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
  flex: none;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}
@media (max-width: 1023px) {
  .table-toolbar,
  .table-toolbar__main {
    flex-direction: column;
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
