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
 * - below 1024px the search has a row of its own, the filters keep to one row that scrolls sideways (select menus
 *   open in the body, so the scroll does not clip them), and the last row holds the live indicator on the left and the
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
  .table-toolbar__filters {
    flex-wrap: nowrap;
    gap: 8px;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .table-toolbar__filters::-webkit-scrollbar {
    display: none;
  }
  .table-toolbar__status {
    margin-right: auto;
  }
}
</style>
