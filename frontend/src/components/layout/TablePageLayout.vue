<template>
  <div
    class="table-page-layout"
    :class="{
      'mobile-mode': isMobile && !tableLayout,
      'table-page-layout--table': tableLayout,
      'table-page-layout--region': tableLayout && isMobile && region
    }"
  >
    <!-- 固定区域：操作按钮 -->
    <div v-if="$slots.actions" class="layout-section-fixed">
      <slot name="actions" />
    </div>

    <!-- 固定区域：搜索和过滤器 -->
    <div v-if="$slots.filters" class="layout-section-fixed">
      <slot name="filters" />
    </div>

    <!-- 滚动区域：表格 -->
    <div class="layout-section-scrollable">
      <div class="card table-scroll-container">
        <slot name="table" />
      </div>
    </div>

    <!-- 固定区域：分页器 -->
    <div v-if="$slots.pagination" class="layout-section-fixed">
      <slot name="pagination" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, provide } from 'vue'
import { useShortViewport, useTableViewport } from '@/composables/useTableViewport'
import { TABLE_REGION_KEY } from './tableRegion'

const props = defineProps<{
  /**
   * The table layout below 1024px (mobile list plan; with DataTable `layout="table"`): the table keeps its card frame
   * on a tablet and runs edge to edge on a phone, and the page fills the visible height, the toolbar and pagination
   * at their own height and the table region the rest, scrolling both ways under its sticky header. Below 500px of
   * visible height the whole page scrolls instead.
   */
  tableLayout?: boolean
  /** With `tableLayout`: the whole page scrolls below 1024px, never the table region on its own. */
  pageScroll?: boolean
}>()

const { desktop } = useTableViewport()
const isMobile = computed(() => !desktop.value)
const short = useShortViewport()
/** Below 1024px the table region is bounded (and windows its rows) only in the table layout, on a tall enough screen. */
const region = computed(() => !props.pageScroll && !short.value)
provide(TABLE_REGION_KEY, computed(() => desktop.value || (!!props.tableLayout && region.value)))
</script>

<style scoped>
/* 桌面端：Flexbox 布局 */
.table-page-layout {
  @apply flex flex-col gap-6;
}

/* 从 1024px 起页面定高（视口减去页头与 lg:p-8 的上下内边距），表格在容器内滚动、表头吸顶。
   页头按实际高度计：64px 的一行加 1px 下边框共 65px，少算 1px 会让整页出现竖向滚动条、占掉表格宽度（7q-3）。
   1024 以下不定高：页面自然滚动，顶栏始终吸顶（7n-2） */
@media (min-width: 1024px) {
  .table-page-layout {
    height: calc(100vh - 65px - 4rem);
  }
}

.layout-section-fixed {
  @apply flex-shrink-0;
}

.layout-section-scrollable {
  @apply flex-1 min-h-0 flex flex-col;
}

/* 表格滚动容器 - 增强版表体滚动方案。颜色来自 styles/tokens.css 的语义变量，与 DataTable 一致。
   relative：「有 N 条变动」提示（PendingChangesPill）在宽屏以它为定位框，落在表头带内 */
.table-scroll-container {
  @apply relative flex flex-col overflow-hidden h-full bg-surface rounded-2xl border border-line-strong shadow-sm;
}

.table-scroll-container :deep(.table-wrapper) {
  @apply flex-1 overflow-x-auto overflow-y-auto;
  /* 确保横向滚动条显示在最底部 */
  scrollbar-gutter: stable;
}

.table-scroll-container :deep(table) {
  @apply w-full;
  min-width: max-content; /* 关键：确保表格宽度根据内容撑开，从而触发横向滚动 */
  display: table; /* 使用标准 table 布局以支持 sticky 列 */
}

.table-scroll-container :deep(thead) {
  @apply bg-surface-header;
}

.table-scroll-container :deep(tbody) {
  /* 保持默认 table-row-group 显示，不使用 block */
}

.table-scroll-container :deep(th) {
  @apply px-5 py-4 text-left text-sm font-medium text-fg-secondary border-b border-line-strong;
}

.table-scroll-container :deep(td) {
  @apply px-5 py-4 text-sm text-fg-primary border-b border-line-row;
}

/* The table layout below 1024px (mobile list plan). Sections 12px apart. In the bounded region the page is the
   visible height less the header (64px and its 1px border) and main's padding (16px, 24px from 768px): dvh follows
   the browser's address bar, vh is the fallback. The table region alone scrolls; nothing outside it does. */
@media (max-width: 1023px) {
  .table-page-layout--table {
    gap: 12px;
  }
  .table-page-layout--table:not(.table-page-layout--region) .layout-section-scrollable {
    flex: none;
  }
  .table-page-layout--region {
    height: calc(100vh - 65px - 2rem);
    height: calc(100dvh - 65px - 2rem);
  }
}
@media (min-width: 768px) and (max-width: 1023px) {
  .table-page-layout--region {
    height: calc(100vh - 65px - 3rem);
    height: calc(100dvh - 65px - 3rem);
  }
}
/* A phone: the table runs to the screen's edges, without the card's rounded frame. */
@media (max-width: 767px) {
  .table-page-layout--table .table-scroll-container {
    margin-inline: -16px;
    border-inline: 0;
    border-radius: 0;
    box-shadow: none;
  }
}

/* 移动端：恢复正常滚动；各区之间按原型收紧到 12px */
.table-page-layout.mobile-mode {
  @apply gap-3;
}

.table-page-layout.mobile-mode .table-scroll-container {
  @apply h-auto overflow-visible border-none shadow-none bg-transparent;
}

.table-page-layout.mobile-mode .layout-section-scrollable {
  @apply flex-none min-h-fit;
}

.table-page-layout.mobile-mode .table-scroll-container :deep(table) {
  @apply flex-none;
  display: table;
  min-width: 100%;
}
</style>
