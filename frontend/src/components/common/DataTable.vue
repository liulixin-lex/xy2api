<template>
  <div v-if="!showsTable" ref="mobileRootRef" class="table-cards" :aria-busy="loading ? 'true' : undefined">
    <!-- Below 1024px (spec 8.6, prototype): cards in one column, two from 768px. A card's head holds the row's leading
         column (and the select column before it), and at its right any column marked `cardHeadEnd`; the other columns
         follow as label-over-value pairs in two columns, the actions sit in the foot. A page with its own card design fills the #card slot instead; `cell` draws any
         column's cell, by its key, the way the table does. -->
    <template v-if="showSkeleton">
      <div v-for="i in 5" :key="i" class="table-card">
        <div class="table-card__head">
          <div class="h-4 w-40 animate-pulse rounded bg-surface-pill"></div>
        </div>
        <div class="table-card__fields">
          <div v-for="column in cardFields" :key="column.key" class="table-card__field">
            <div class="h-3 w-16 animate-pulse rounded bg-surface-pill"></div>
            <div class="h-4 w-24 animate-pulse rounded bg-surface-pill"></div>
          </div>
        </div>
      </div>
    </template>

    <template v-else-if="!data || data.length === 0">
      <div class="table-cards__full rounded-lg border border-line-strong bg-surface p-12 text-center">
        <slot name="empty">
          <div class="flex flex-col items-center">
            <Icon
              name="inbox"
              size="xl"
              class="mb-4 h-12 w-12 text-fg-muted"
            />
            <p class="text-lg font-medium text-fg-primary">
              {{ t('empty.noData') }}
            </p>
          </div>
        </slot>
      </div>
    </template>

    <template v-else>
      <div v-if="selectable || mobileSortColumns.length || pageSorts.length || $slots['mobile-sort-extra']" class="table-cards__full flex flex-wrap items-center justify-end gap-2 px-1">
        <!-- 手机端没有表头，排序放到卡片上方 -->
        <div v-if="mobileSortColumns.length || pageSorts.length" class="mr-auto flex min-w-0 items-center gap-2" data-test="mobile-sort">
          <label :for="mobileSortSelectId" class="flex-shrink-0 text-sm font-medium text-fg-secondary">
            {{ t('common.sortBy') }}
          </label>
          <select
            :id="mobileSortSelectId"
            class="input h-9 min-w-0 max-w-[11rem] py-1 text-sm"
            :value="pageSort?.key || sortKey"
            data-test="mobile-sort-key"
            @change="onMobileSortChange(($event.target as HTMLSelectElement).value)"
          >
            <option v-if="!sortKey && !pageSort" value="" disabled>{{ t('common.sortPlaceholder') }}</option>
            <option v-for="column in mobileSortColumns" :key="column.key" :value="column.key">{{ column.label }}</option>
            <option v-for="option in pageSorts" :key="option.key" :value="option.key">{{ option.label }}</option>
          </select>
          <button
            v-if="pageSort || sortKey"
            type="button"
            class="table-cards__sort-order btn btn-secondary btn-icon h-9 w-9 flex-shrink-0"
            :aria-label="mobileSortOrder === 'asc' ? t('common.sortAscending') : t('common.sortDescending')"
            :title="mobileSortOrder === 'asc' ? t('common.sortAscending') : t('common.sortDescending')"
            data-test="mobile-sort-order"
            @click="pageSort ? choosePageSort(pageSort.key) : handleSort(sortKey)"
          >
            <Icon :name="mobileSortOrder === 'asc' ? 'arrowUp' : 'arrowDown'" size="sm" />
          </button>
        </div>
        <slot name="mobile-sort-extra" />
        <label v-if="selectable" class="flex items-center gap-2 text-sm font-medium text-fg-secondary">
          <input
            type="checkbox"
            class="h-4 w-4 rounded border-line-control bg-surface-control text-primary-600 focus:ring-primary-500"
            :checked="allVisibleSelected"
            :indeterminate="someVisibleSelected"
            :disabled="visibleRowKeys.length === 0"
            data-test="select-all-mobile"
            @change="toggleAllVisible(($event.target as HTMLInputElement).checked)"
          />
          <span>{{ t('common.selectAll') }}</span>
        </label>
      </div>
      <div
        v-for="(row, index) in sortedData"
        :key="resolveRowKey(row, index)"
        class="table-card"
        :class="[
          { 'is-selected': selectable && isRowSelected(row, index), 'cursor-pointer': clickableRows },
          rowClass?.(row, index)
        ]"
        @click="clickableRows && emit('rowClick', row)"
      >
        <slot v-if="$slots.card" name="card" :row="row" :index="index" :cell="CardCell" />
        <template v-else>
          <div class="table-card__head">
            <input
              v-if="selectable"
              type="checkbox"
              class="mt-0.5 h-4 w-4 flex-none rounded border-line-control bg-surface-control text-primary-600 focus:ring-primary-500"
              :checked="isRowSelected(row, index)"
              :disabled="!isRowSelectable(row)"
              :aria-label="getRowSelectionLabel(row, index)"
              data-test="select-row"
              @click.stop
              @change="toggleRowSelection(row, index, ($event.target as HTMLInputElement).checked)"
            />
            <div
              v-for="column in cardHead"
              :key="column.key"
              :data-field="column.key"
              class="table-card__title"
              :class="{ 'table-card__title--select': column.key === 'select' }"
            >
              <slot :name="`cell-${column.key}`" :row="row" :value="row[column.key]" :expanded="actionsExpanded">
                {{ column.formatter ? column.formatter(row[column.key], row) : row[column.key] }}
              </slot>
            </div>
            <div v-for="column in cardHeadEnd" :key="column.key" :data-field="column.key" class="table-card__end">
              <slot :name="`cell-${column.key}`" :row="row" :value="row[column.key]" :expanded="actionsExpanded">
                {{ column.formatter ? column.formatter(row[column.key], row) : row[column.key] }}
              </slot>
            </div>
          </div>
          <dl v-if="cardFields.length" class="table-card__fields">
            <div v-for="column in cardFields" :key="column.key" :data-field="column.key" class="table-card__field" :class="{ 'col-span-full': column.cardFullRow }">
              <dt class="table-card__label">{{ column.label }}</dt>
              <dd class="table-card__value">
                <slot :name="`cell-${column.key}`" :row="row" :value="row[column.key]" :expanded="actionsExpanded">
                  {{ column.formatter ? column.formatter(row[column.key], row) : row[column.key] }}
                </slot>
              </dd>
            </div>
          </dl>
          <div v-if="hasActionsColumn" class="table-card__foot">
            <slot name="cell-actions" :row="row" :value="row['actions']" :expanded="actionsExpanded"></slot>
          </div>
        </template>
      </div>
    </template>

    <!-- Reload with rows on screen: a thin bar on the top edge of the list; it takes no layout space and the cards stay
         usable. Last in the list, so nothing before the first card changes when it comes and goes. -->
    <div v-if="refreshing" class="table-progress table-progress--cards" aria-hidden="true" data-test="table-progress">
      <div class="table-progress__bar"></div>
    </div>
  </div>

  <div
    v-else
    ref="tableWrapperRef"
    class="table-wrapper"
    :class="{
      'actions-expanded': actionsExpanded,
      'is-scrollable': isScrollable,
      'dt-layout-table': layout === 'table',
      'dt-builtin-select': layout === 'table' && selectable
    }"
    :aria-busy="loading ? 'true' : undefined"
  >
    <!-- Reload with rows on screen: a thin bar on the top edge of the table; it takes no layout space. -->
    <div v-if="refreshing" class="table-progress table-progress--table" aria-hidden="true" data-test="table-progress">
      <div class="table-progress__bar"></div>
    </div>
    <table class="w-full min-w-max">
      <thead class="table-header bg-surface-header">
        <tr v-for="(headerRow, rowIndex) in headerRows" :key="rowIndex">
          <th
            v-if="selectable && rowIndex === 0"
            scope="col"
            :rowspan="headerRows.length"
            :class="['sticky-header-cell w-11 min-w-11 px-3 py-3 text-center', builtinSelectStickyClass]"
          >
            <label class="dt-check">
              <input
                type="checkbox"
                class="h-4 w-4 rounded border-line-control bg-surface-control text-primary-600 focus:ring-primary-500"
                :checked="allVisibleSelected"
                :indeterminate="someVisibleSelected"
                :disabled="visibleRowKeys.length === 0"
                :aria-label="t('common.selectAll')"
                data-test="select-all"
                @change="toggleAllVisible(($event.target as HTMLInputElement).checked)"
              />
            </label>
          </th>
          <template v-for="cell in headerRow" :key="cell.key">
            <th
              v-if="cell.kind === 'group'"
              scope="colgroup"
              :colspan="cell.span"
              :class="['sticky-header-cell pb-1 pt-3 text-center text-xs font-medium uppercase tracking-wider text-fg-secondary', getAdaptivePaddingClass()]"
            >
              <slot :name="`header-group-${cell.group}`" :group="cell.group" />
            </th>
            <th
              v-else
              scope="col"
              :rowspan="cell.rowspan"
              :aria-sort="cell.column.ariaSort ?? (cell.column.sortable ? getColumnAriaSort(cell.column.key) : undefined)"
              :class="[
                'sticky-header-cell text-left text-xs font-medium uppercase tracking-wider text-fg-secondary',
                headerRows.length > 1 && cell.rowspan === 1 ? 'pb-3 pt-1' : 'py-3',
                getAdaptivePaddingClass(),
                { 'cursor-pointer hover:bg-surface-control-hover': cell.column.sortable },
                getStickyColumnClass(cell.column, cell.index),
                cell.column.class,
                cell.column.headerClass
              ]"
              @click="cell.column.sortable && handleSort(cell.column.key)"
            >
              <!-- A sortable header is a button across its whole cell (mobile list plan): a click anywhere on the cell, or
                   Enter / Space on the focused button, sorts; the th keeps scope and aria-sort. -->
              <component
                :is="cell.column.sortable ? 'button' : 'div'"
                :type="cell.column.sortable ? 'button' : undefined"
                :class="['flex items-center space-x-1', getHeaderContentAlignmentClass(cell.column), { 'dt-sort': cell.column.sortable }]"
                :data-test="cell.column.sortable ? `sort-${cell.column.key}` : undefined"
                @click.stop="cell.column.sortable && handleSort(cell.column.key)"
              >
                <slot
                  :name="`header-${cell.column.key}`"
                  :column="cell.column"
                  :sort-key="sortKey"
                  :sort-order="sortOrder"
                >
                  <span>{{ cell.column.label }}</span>
                </slot>
                <span
                  v-if="cell.column.sortable"
                  class="sort-indicator inline-flex h-5 w-4 flex-col items-center justify-center"
                  :class="{ 'is-sorted': sortKey === cell.column.key }"
                  aria-hidden="true"
                >
                  <svg
                    class="h-2.5 w-2.5"
                    :class="getSortIndicatorClass(cell.column.key, 'asc')"
                    fill="currentColor"
                    viewBox="0 0 10 10"
                  >
                    <path d="M5 2L1.5 6.5h7L5 2z" />
                  </svg>
                  <svg
                    class="-mt-0.5 h-2.5 w-2.5"
                    :class="getSortIndicatorClass(cell.column.key, 'desc')"
                    fill="currentColor"
                    viewBox="0 0 10 10"
                  >
                    <path d="M5 8L1.5 3.5h7L5 8z" />
                  </svg>
                </span>
              </component>
            </th>
          </template>
        </tr>
      </thead>
      <tbody class="table-body bg-surface">
        <!-- Loading skeleton: only while there is nothing to show yet -->
        <tr v-if="showSkeleton" v-for="i in 5" :key="i" class="table-row-border">
          <td v-if="selectable" :class="['w-11 min-w-11 px-3 py-4', builtinSelectStickyClass]">
            <div class="mx-auto h-4 w-4 animate-pulse rounded bg-surface-pill"></div>
          </td>
          <td v-for="column in columns" :key="column.key" :class="['whitespace-nowrap py-4', getAdaptivePaddingClass()]">
            <div class="animate-pulse">
              <div class="h-4 w-3/4 rounded bg-surface-pill"></div>
            </div>
          </td>
        </tr>

        <!-- Empty state -->
        <tr v-else-if="!data || data.length === 0" class="table-row-border">
          <td
            :colspan="tableColumnCount"
            :class="['py-12 text-center text-fg-secondary', getAdaptivePaddingClass()]"
          >
            <slot name="empty">
              <div class="flex flex-col items-center">
                <Icon
                  name="inbox"
                  size="xl"
                  class="mb-4 h-12 w-12 text-fg-muted"
                />
                <p class="text-lg font-medium text-fg-primary">
                  {{ t('empty.noData') }}
                </p>
              </div>
            </slot>
          </td>
        </tr>

        <!-- Data rows: windowed when large, fully rendered when small (shared row/cell template) -->
        <template v-else>
          <tr v-if="virtualPaddingTop > 0" aria-hidden="true">
            <td :colspan="tableColumnCount"
                :style="{ height: virtualPaddingTop + 'px', padding: 0, border: 'none' }">
            </td>
          </tr>
          <DataTableRow
            v-for="item in renderRows"
            :key="resolveRowKey(item.row, item.index)"
            :ref="item.measure ? measureRowComponent : undefined"
            :data-row-id="resolveRowKey(item.row, item.index)"
            :data-index="item.index"
            :row="item.row"
            :index="item.index"
            :columns="columns"
            :sticky-classes="stickyColumnClasses"
            :select-class="builtinSelectStickyClass"
            :padding-class="adaptivePaddingClass"
            :selectable="selectable"
            :selected="selectable && isRowSelected(item.row, item.index)"
            :selection-disabled="!isRowSelectable(item.row)"
            :selection-label="selectable ? getRowSelectionLabel(item.row, item.index) : ''"
            :clickable="clickableRows"
            :expanded="actionsExpanded"
            :cell-slots="$slots"
            :row-class="rowClass"
            @toggle="toggleRowSelection"
            @row-click="onRowClick"
          />
          <tr v-if="virtualPaddingBottom > 0" aria-hidden="true">
            <td :colspan="tableColumnCount"
                :style="{ height: virtualPaddingBottom + 'px', padding: 0, border: 'none' }">
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { computed, inject, ref, onMounted, onUnmounted, watch, nextTick, useId, useSlots, type ComponentPublicInstance, type FunctionalComponent } from 'vue'
import { useVirtualizer, observeElementRect as observeElementRectDefault, type Virtualizer } from '@tanstack/vue-virtual'
import { useI18n } from 'vue-i18n'
import type { Column } from './types'
import Icon from '@/components/icons/Icon.vue'
import DataTableRow from './DataTableRow'
import { TABLE_REGION_KEY } from '@/components/layout/tableRegion'

const { t } = useI18n()

// Cards below 1024px, as the shared page layout and the changes pill switch there too (spec 8.6, prototype).
const desktopViewportQuery = '(min-width: 1024px)'
const isDesktopViewport = ref(
  typeof window === 'undefined' ? true : window.matchMedia(desktopViewportQuery).matches
)

const emit = defineEmits<{
  sort: [key: string, order: 'asc' | 'desc']
  rowClick: [row: any]
  'update:selectedKeys': [keys: Array<string | number>]
  selectionChange: [keys: Array<string | number>]
  pageSort: [key: string]
}>()

// 表格容器引用
const tableWrapperRef = ref<HTMLElement | null>(null)
const mobileRootRef = ref<HTMLElement | null>(null)
const isScrollable = ref(false)
const actionsColumnNeedsExpanding = ref(false)

// --- 虚拟滚动「整表空白」根治 ---
// 根因:本组件根 .table-wrapper 为 flex:1 / min-h-0,高度由父级 flex 链决定。@tanstack 虚拟化器
// 仅在 observeElementRect 回调里写 scrollRect;一旦该回调读到 0 高度(加载瞬间 flex 未结算,或
// 滚动中动态行高校正触发的 reflow),scrollRect 被钉死为 0 → calculateRange 返回 null → 整表空白。
// 对策(见下方 virtualizer 选项):
//   1) 覆写 observeElementRect,直接丢弃 height<=0 的读数,scrollRect 永不被钉成 0;
//   2) initialRect 给一屏兜底高度,首个有效读数到来前也有行可渲染,绝不空白。
// 兜底高度:表格区域大致 = 视口高度 - 顶栏/外边距/筛选/分页 ≈ 320px
const estimatedViewportHeight = () => {
  if (typeof window === 'undefined') return 600
  return Math.max(window.innerHeight - 320, 400)
}

// 覆写默认 observeElementRect:过滤掉 0 高度读数(根治整表空白的关键)
const observeElementRectNonZero = (
  instance: any,
  cb: (rect: { width: number; height: number }) => void
) => observeElementRectDefault(instance, (rect) => {
  if (rect.height > 0) cb(rect)
})

// 检查是否可滚动
const checkScrollable = () => {
  if (tableWrapperRef.value) {
    isScrollable.value = tableWrapperRef.value.scrollWidth > tableWrapperRef.value.clientWidth
  }
}

// 检查操作列是否需要展开
const checkActionsColumnWidth = () => {
  if (!props.expandableActions) {
    actionsColumnNeedsExpanding.value = false
    actionsExpanded.value = false
    return
  }
  if (!tableWrapperRef.value) return

  // 查找第一行的操作列单元格
  const firstActionCell = tableWrapperRef.value.querySelector('tbody tr:first-child td:last-child')
  if (!firstActionCell) return

  // 查找操作列内容的容器div
  const actionsContainer = firstActionCell.querySelector('div')
  if (!actionsContainer) return

  // 临时展开以测量完整宽度
  const wasExpanded = actionsExpanded.value
  actionsExpanded.value = true

  // 等待DOM更新
  nextTick(() => {
    // 测量所有按钮的总宽度
    const actionItems = actionsContainer.querySelectorAll('button, a, [role="button"]')
    if (actionItems.length <= 2) {
      actionsColumnNeedsExpanding.value = false
      actionsExpanded.value = wasExpanded
      return
    }

    // 计算所有按钮的总宽度（包括gap）
    let totalWidth = 0
    actionItems.forEach((item, index) => {
      totalWidth += (item as HTMLElement).offsetWidth
      if (index < actionItems.length - 1) {
        totalWidth += 4 // gap-1 = 4px
      }
    })

    // 获取单元格可用宽度（减去padding）
    const cellWidth = (firstActionCell as HTMLElement).clientWidth - 32 // 减去左右padding

    // 如果总宽度超过可用宽度，需要展开功能
    actionsColumnNeedsExpanding.value = totalWidth > cellWidth

    // 恢复原来的展开状态
    actionsExpanded.value = wasExpanded
  })
}

// 监听尺寸变化
let resizeObserver: ResizeObserver | null = null
let resizeHandler: (() => void) | null = null
let desktopViewportMediaQuery: MediaQueryList | null = null
let desktopViewportListener: ((event: MediaQueryListEvent) => void) | null = null

const detachDesktopTableTracking = () => {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (resizeHandler) {
    window.removeEventListener('resize', resizeHandler)
    resizeHandler = null
  }
}

const attachDesktopTableTracking = () => {
  checkScrollable()
  checkActionsColumnWidth()
  if (tableWrapperRef.value && typeof ResizeObserver !== 'undefined') {
    const wrapper = tableWrapperRef.value
    resizeObserver = new ResizeObserver((entries) => {
      checkScrollable()
      // Only the wrapper's own size changes the room of the actions column; a change of the table alone just
      // re-checks the sideways scroll (spec 7g-4).
      if (entries.some((entry) => entry.target === wrapper)) checkActionsColumnWidth()
    })
    resizeObserver.observe(wrapper)
    // The table too: a page may narrow its columns after the wrapper's resize (the accounts page recomputes its column
    // widths from the new width), and only the table's own size change tells that it no longer scrolls sideways.
    const table = wrapper.querySelector(':scope > table')
    if (table) resizeObserver.observe(table)
  } else {
    // 降级方案：不支持 ResizeObserver 时使用 window resize
    resizeHandler = () => {
      checkScrollable()
      checkActionsColumnWidth()
    }
    window.addEventListener('resize', resizeHandler)
  }
}

onMounted(() => {
  if (typeof window !== 'undefined') {
    desktopViewportMediaQuery = window.matchMedia(desktopViewportQuery)
    isDesktopViewport.value = desktopViewportMediaQuery.matches
    desktopViewportListener = (event: MediaQueryListEvent) => {
      isDesktopViewport.value = event.matches
    }
    if (typeof desktopViewportMediaQuery.addEventListener === 'function') {
      desktopViewportMediaQuery.addEventListener('change', desktopViewportListener)
    } else {
      desktopViewportMediaQuery.addListener(desktopViewportListener)
    }
  }
})

onUnmounted(() => {
  detachDesktopTableTracking()
  if (desktopViewportMediaQuery && desktopViewportListener) {
    if (typeof desktopViewportMediaQuery.removeEventListener === 'function') {
      desktopViewportMediaQuery.removeEventListener('change', desktopViewportListener)
    } else {
      desktopViewportMediaQuery.removeListener(desktopViewportListener)
    }
    desktopViewportListener = null
  }
  desktopViewportMediaQuery = null
})

interface Props {
  /**
   * `table` (mobile list plan): the table at every width, cards never. Below 1024px it keeps the sticky header, the
   * frozen columns and their edge lines, the size tracking and, inside a bounded table region, the windowing; below
   * 768px the frozen identity column takes `--table-identity-width` (clamp(120px, 36vw, 160px)) and a frozen actions
   * column 44px. `cards` is the old narrow layout, kept only until every page has moved (then it and this prop go).
   */
  layout?: 'cards' | 'table'
  columns: Column[]
  data: any[]
  loading?: boolean
  stickyFirstColumn?: boolean
  stickyActionsColumn?: boolean
  expandableActions?: boolean
  actionsCount?: number // 操作按钮总数，用于判断是否需要展开功能
  rowKey?: string | ((row: any) => string | number)
  /**
   * Default sort configuration (only applied when there is no persisted sort state)
   */
  defaultSortKey?: string
  defaultSortOrder?: 'asc' | 'desc'
  /**
   * Persist sort state (key + order) to localStorage using this key.
   * If provided, DataTable will load the stored sort state on mount.
   */
  sortStorageKey?: string
  /**
   * Enable server-side sorting mode. When true, clicking sort headers
   * will emit 'sort' events instead of performing client-side sorting.
   */
  serverSideSort?: boolean
  /** Emit 'rowClick' on row/card click and show pointer cursor (interactive cells should @click.stop) */
  clickableRows?: boolean
  /** Estimated row height in px for the virtualizer (default 56) */
  estimateRowHeight?: number
  /** Number of rows to render beyond the visible area (default 5) */
  overscan?: number
  /**
   * Only virtualize when the row count exceeds this threshold (default 100).
   * Smaller lists render in full, avoiding the scroll-compensation jank caused by
   * estimated-vs-actual row heights when rows have variable height.
   */
  virtualizeThreshold?: number
  /** Enable controlled row selection. Stable row keys are strongly recommended. */
  selectable?: boolean
  /** Restrict row and current-page selection; omitted means every row is selectable. */
  rowSelectable?: (row: any) => boolean
  /** Selected row keys. Keys outside the current data page are preserved. */
  selectedKeys?: Array<string | number>
  /**
   * Sorts the parent applies to the current page only. They are offered in the mobile sort bar next to the column
   * sorts; choosing one emits `pageSort` with its key (the parent cycles the order), choosing a column emits `pageSort`
   * with '' first.
   */
  pageSorts?: Array<{ key: string; label: string }>
  pageSort?: { key: string; order: 'asc' | 'desc' } | null
  /** Accessible label for a row selection checkbox. */
  selectionLabel?: string | ((row: any) => string)
  /**
   * Extra classes for a desktop row and a mobile card (for example dimming a row that is transitioning or no longer
   * matches the filter). A desktop row calls it in its own render, so only rows whose answer changes re-render. Pass a
   * stable function defined once in setup: an inline arrow in the template is a new function on every render of the
   * parent and re-renders every row.
   */
  rowClass?: (row: any, index: number) => unknown
}

const props = withDefaults(defineProps<Props>(), {
  layout: 'cards',
  loading: false,
  stickyFirstColumn: true,
  stickyActionsColumn: true,
  expandableActions: true,
  defaultSortOrder: 'asc',
  serverSideSort: false,
  selectable: false,
  selectedKeys: () => [],
  pageSorts: () => [],
  pageSort: null
})

/** The table is mounted: always in the table layout, from 1024px in the cards layout. */
const showsTable = computed(() => props.layout === 'table' || isDesktopViewport.value)
/** Below 1024px rows are windowed only inside a bounded table region (TablePageLayout); a page that scrolls renders them all. */
const tableRegion = inject(TABLE_REGION_KEY, null)
const windowedHere = computed(() => isDesktopViewport.value || (props.layout === 'table' && tableRegion?.value === true))

// A reload keeps the rows it already has (same DOM nodes, same scroll position) and shows a thin progress bar; the
// skeleton is only for a load with nothing on screen yet. The table stays clickable throughout (no inert).
const hasRows = computed(() => (props.data?.length ?? 0) > 0)
const showSkeleton = computed(() => props.loading && !hasRows.value)
const refreshing = computed(() => props.loading && hasRows.value)

const sortKey = ref<string>('')
const sortOrder = ref<'asc' | 'desc'>('asc')
const actionsExpanded = ref(false)

type PersistedSortState = {
  key: string
  order: 'asc' | 'desc'
}

const collator = new Intl.Collator(undefined, {
  numeric: true,
  sensitivity: 'base'
})

const getSortableKeys = () => {
  const keys = new Set<string>()
  for (const col of props.columns) {
    if (col.sortable) keys.add(col.key)
  }
  return keys
}

const normalizeSortKey = (candidate: string) => {
  if (!candidate) return ''
  const sortableKeys = getSortableKeys()
  return sortableKeys.has(candidate) ? candidate : ''
}

const normalizeSortOrder = (candidate: any): 'asc' | 'desc' => {
  return candidate === 'desc' ? 'desc' : 'asc'
}

const readPersistedSortState = (): PersistedSortState | null => {
  if (!props.sortStorageKey) return null
  try {
    const raw = localStorage.getItem(props.sortStorageKey)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<PersistedSortState>
    const key = normalizeSortKey(typeof parsed.key === 'string' ? parsed.key : '')
    if (!key) return null
    return { key, order: normalizeSortOrder(parsed.order) }
  } catch (e) {
    console.error('[DataTable] Failed to read persisted sort state:', e)
    return null
  }
}

const writePersistedSortState = (state: PersistedSortState) => {
  if (!props.sortStorageKey) return
  try {
    localStorage.setItem(props.sortStorageKey, JSON.stringify(state))
  } catch (e) {
    console.error('[DataTable] Failed to persist sort state:', e)
  }
}

const resolveInitialSortState = (): PersistedSortState | null => {
  const persisted = readPersistedSortState()
  if (persisted) return persisted

  const key = normalizeSortKey(props.defaultSortKey || '')
  if (!key) return null
  return { key, order: normalizeSortOrder(props.defaultSortOrder) }
}

const applySortState = (state: PersistedSortState | null) => {
  if (!state) return
  sortKey.value = state.key
  sortOrder.value = state.order
}

const getSortIndicatorClass = (key: string, order: 'asc' | 'desc') => {
  return sortKey.value === key && sortOrder.value === order
    ? 'text-primary-600 dark:text-primary-400'
    : 'text-fg-muted opacity-50 transition-colors'
}

const getColumnAriaSort = (key: string) => {
  if (sortKey.value !== key) return 'none'
  return sortOrder.value === 'asc' ? 'ascending' : 'descending'
}

const getHeaderContentAlignmentClass = (column: Column) => {
  const className = column.class || ''
  if (className.includes('text-center')) return 'justify-center'
  if (className.includes('text-right')) return 'justify-end'
  return 'justify-start'
}

const isNullishOrEmpty = (value: any) => value === null || value === undefined || value === ''

const toFiniteNumberOrNull = (value: any): number | null => {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null
  if (typeof value === 'boolean') return value ? 1 : 0
  if (typeof value === 'string') {
    const trimmed = value.trim()
    if (!trimmed) return null
    const n = Number(trimmed)
    return Number.isFinite(n) ? n : null
  }
  return null
}

const toSortableString = (value: any): string => {
  if (value === null || value === undefined) return ''
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (value instanceof Date) return value.toISOString()
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

const compareSortValues = (a: any, b: any): number => {
  const aEmpty = isNullishOrEmpty(a)
  const bEmpty = isNullishOrEmpty(b)
  if (aEmpty && bEmpty) return 0
  if (aEmpty) return 1
  if (bEmpty) return -1

  const aNum = toFiniteNumberOrNull(a)
  const bNum = toFiniteNumberOrNull(b)
  if (aNum !== null && bNum !== null) {
    if (aNum === bNum) return 0
    return aNum < bNum ? -1 : 1
  }

  const aStr = toSortableString(a)
  const bStr = toSortableString(b)
  const res = collator.compare(aStr, bStr)
  if (res === 0) return 0
  return res < 0 ? -1 : 1
}
const resolveStableRowKey = (row: any): string | number | undefined => {
  if (typeof props.rowKey === 'function') {
    const key = props.rowKey(row)
    return key ?? undefined
  }
  if (typeof props.rowKey === 'string' && props.rowKey) {
    const key = row?.[props.rowKey]
    return key ?? undefined
  }
  const key = row?.id
  return key ?? undefined
}

const resolveRowKey = (row: any, index: number) => resolveStableRowKey(row) ?? index

const dataColumns = computed(() => props.columns.filter((column) => column.key !== 'actions'))
const slots = useSlots()
/**
 * One column's cell for a row as the table draws it (its #cell-<key> slot, else the value), the column named by its
 * key: for a page's own card, `<component :is="cell" column="usage" :row="row" />`.
 */
const CardCell: FunctionalComponent<{ column: string; row: any }> = ({ column: key, row }) => {
  const column = props.columns.find((item) => item.key === key)!
  const slot = slots[`cell-${column.key}`]
  if (slot) return slot({ row, value: row[column.key], expanded: actionsExpanded.value })
  return String((column.formatter ? column.formatter(row[column.key], row) : row[column.key]) ?? '')
}
CardCell.props = ['column', 'row']
/** A card's head: the leading column, with the page's own select column before it when it has one. */
const cardHead = computed(() => dataColumns.value.slice(0, hasSelectColumn.value ? 2 : 1))
const cardHeadEnd = computed(() => dataColumns.value.slice(cardHead.value.length).filter((column) => column.cardHeadEnd))
const cardFields = computed(() => dataColumns.value.slice(cardHead.value.length).filter((column) => !column.cardHeadEnd))
const mobileSortColumns = computed(() => dataColumns.value.filter((column) => column.sortable && column.label))
const mobileSortSelectId = `data-table-sort-${useId()}`
// A sort picked here is the table's own sort change: back to the first row (the parent re-orders its page).
const choosePageSort = (key: string) => {
  scrollToTop()
  emit('pageSort', key)
}
const onMobileSortChange = (key: string) => {
  if (props.pageSorts.some((option) => option.key === key)) {
    choosePageSort(key)
    return
  }
  if (props.pageSort) emit('pageSort', '')
  if (key && key !== sortKey.value) handleSort(key)
}
const mobileSortOrder = computed(() => props.pageSort?.order ?? sortOrder.value)

// Header rows: one row, or two when some columns share a group header (the group cell spans them, other columns
// span both rows).
type HeaderCell =
  | { kind: 'column'; key: string; column: Column; index: number; rowspan: number }
  | { kind: 'group'; key: string; group: string; span: number }
const headerRows = computed<HeaderCell[][]>(() => {
  const columns = props.columns
  if (!columns.some((column) => column.group)) {
    return [columns.map((column, index) => ({ kind: 'column' as const, key: column.key, column, index, rowspan: 1 }))]
  }
  const top: HeaderCell[] = []
  const bottom: HeaderCell[] = []
  columns.forEach((column, index) => {
    if (!column.group) {
      top.push({ kind: 'column', key: column.key, column, index, rowspan: 2 })
      return
    }
    bottom.push({ kind: 'column', key: column.key, column, index, rowspan: 1 })
    if (columns[index - 1]?.group === column.group) return
    let span = 1
    while (columns[index + span]?.group === column.group) span++
    top.push({ kind: 'group', key: `group:${column.group}:${index}`, group: column.group, span })
  })
  return [top, bottom]
})
const columnsSignature = computed(() =>
  props.columns.map((column) => `${column.key}:${column.sortable ? '1' : '0'}`).join('|')
)

// The table's size tracking (sideways scroll, the actions column's room) runs while the table is mounted.
watch(
  showsTable,
  async (mounted) => {
    detachDesktopTableTracking()
    if (!mounted) return
    await nextTick()
    attachDesktopTableTracking()
  },
  { immediate: true, flush: 'post' }
)

// 数据/列变化时重新检查滚动状态
// 注意：不能监听 actionsExpanded，因为 checkActionsColumnWidth 会临时修改它，会导致无限循环
watch(
  [() => props.data.length, columnsSignature],
  async () => {
    await nextTick()
    checkScrollable()
    checkActionsColumnWidth()
  },
  { flush: 'post' }
)

// 单独监听展开状态变化，只更新滚动状态
watch(actionsExpanded, async () => {
  await nextTick()
  checkScrollable()
})

const handleSort = (key: string) => {
  scrollToTop()
  let newOrder: 'asc' | 'desc' = 'asc'
  if (sortKey.value === key) {
    newOrder = sortOrder.value === 'asc' ? 'desc' : 'asc'
  }

  if (props.serverSideSort) {
    // Server-side sort mode: emit event and update internal state for UI feedback
    sortKey.value = key
    sortOrder.value = newOrder
    emit('sort', key, newOrder)
  } else {
    // Client-side sort mode: just update internal state
    sortKey.value = key
    sortOrder.value = newOrder
  }
}

const sortedData = computed(() => {
  // Server-side sort mode: return data as-is (server handles sorting)
  if (props.serverSideSort || !sortKey.value || !props.data) return props.data

  const key = sortKey.value
  const order = sortOrder.value

  // Stable sort (tie-break with original index) to avoid jitter when values are equal.
  return props.data
    .map((row, index) => ({ row, index }))
    .sort((a, b) => {
      const cmp = compareSortValues(a.row?.[key], b.row?.[key])
      if (cmp !== 0) return order === 'asc' ? cmp : -cmp
      return a.index - b.index
    })
    .map(item => item.row)
})

const tableColumnCount = computed(() => props.columns.length + (props.selectable ? 1 : 0))
const selectedKeySet = computed(() => new Set(props.selectedKeys))
const isRowSelectable = (row: any) => props.rowSelectable?.(row) ?? true
const visibleRowKeys = computed(() =>
  (sortedData.value ?? []).flatMap((row, index) => isRowSelectable(row) ? [resolveRowKey(row, index)] : [])
)
const allVisibleSelected = computed(() =>
  visibleRowKeys.value.length > 0
  && visibleRowKeys.value.every((key) => selectedKeySet.value.has(key))
)
const someVisibleSelected = computed(() => {
  if (allVisibleSelected.value) return false
  return visibleRowKeys.value.some((key) => selectedKeySet.value.has(key))
})

const emitSelection = (next: Set<string | number>) => {
  const keys = Array.from(next)
  emit('update:selectedKeys', keys)
  emit('selectionChange', keys)
}

const isRowSelected = (row: any, index: number) =>
  selectedKeySet.value.has(resolveRowKey(row, index))

const getRowSelectionLabel = (row: any, index: number) => {
  if (typeof props.selectionLabel === 'function') return props.selectionLabel(row)
  if (props.selectionLabel) return props.selectionLabel
  return `${t('common.selectOption')} ${resolveRowKey(row, index)}`
}

const toggleRowSelection = (row: any, index: number, checked: boolean) => {
  if (!isRowSelectable(row)) return
  const next = new Set(props.selectedKeys)
  const key = resolveRowKey(row, index)
  if (checked) next.add(key)
  else next.delete(key)
  emitSelection(next)
}

const toggleAllVisible = (checked: boolean) => {
  const next = new Set(props.selectedKeys)
  for (const key of visibleRowKeys.value) {
    if (checked) next.add(key)
    else next.delete(key)
  }
  emitSelection(next)
}

// --- Virtual scrolling ---
// 是否启用虚拟化:仅桌面端且行数超过阈值时开启。小列表全量渲染,彻底绕开虚拟器的
// 估算/测量/滚动补偿链路,消除可变行高导致的滚动抖动。
const shouldVirtualize = computed(() =>
  showsTable.value && windowedHere.value && (sortedData.value?.length ?? 0) > (props.virtualizeThreshold ?? 100)
)

// 行高只取 ResizeObserver 给出的 borderBoxSize。默认实现在 ref 回调里同步读 offsetHeight,
// 会在 Vue 打补丁途中强制布局；这里同步路径直接沿用当前已知高度(已测或估算),
// 真实高度由随后的 ResizeObserver 回调补上。
const measureRowFromObserver = (
  element: Element,
  entry: ResizeObserverEntry | undefined,
  instance: Virtualizer<HTMLElement, Element>
) => {
  if (entry) {
    const box = entry.borderBoxSize?.[0]
    return Math.round(box ? box.blockSize : entry.contentRect.height)
  }
  const index = instance.indexFromElement(element)
  return instance.measurementsCache[index]?.size ?? (props.estimateRowHeight ?? 56)
}

const rowVirtualizer = useVirtualizer(computed(() => ({
  count: shouldVirtualize.value ? (sortedData.value?.length ?? 0) : 0,
  getScrollElement: () => tableWrapperRef.value,
  // 用行主键(与模板 :key 一致)而非默认的 index 作为 itemSizeCache 键,
  // 这样排序/筛选/跨阈值来回都能复用正确的已测行高,而不是残留的按 index 缓存 → 消除高度校正抖动。
  getItemKey: (index: number) => {
    const row = sortedData.value?.[index]
    return row != null ? resolveRowKey(row, index) : index
  },
  estimateSize: () => props.estimateRowHeight ?? 56,
  overscan: props.overscan ?? 5,
  // 兜底高度:首个有效高度读数到来前,先按一屏渲染,避免空白帧
  initialRect: { width: 0, height: estimatedViewportHeight() },
  // 关键:过滤 0 高度读数,杜绝 scrollRect 被钉成 0 → calculateRange 返回 null → 整表空白
  observeElementRect: observeElementRectNonZero,
  // 把测量类 ResizeObserver 回调批到 rAF,避免滚动中同步 reflow 风暴导致的校正抖动/空白
  useAnimationFrameWithResizeObserver: true,
  measureElement: measureRowFromObserver,
})))

const virtualItems = computed(() => rowVirtualizer.value.getVirtualItems())

const virtualPaddingTop = computed(() => {
  const items = virtualItems.value
  return items.length > 0 ? items[0].start : 0
})

const virtualPaddingBottom = computed(() => {
  const items = virtualItems.value
  if (items.length === 0) return 0
  return rowVirtualizer.value.getTotalSize() - items[items.length - 1].end
})

const measureElement = (el: any) => {
  if (el) {
    rowVirtualizer.value.measureElement(el as Element)
  }
}

const measureRowComponent = (instance: any) => {
  measureElement((instance as ComponentPublicInstance | null)?.$el)
}

const onRowClick = (row: any) => emit('rowClick', row)

type RowIdentityToken = string | number | object | symbol

const rowIdentityKeys = computed<RowIdentityToken[]>(() =>
  (sortedData.value ?? []).map((row) => {
    const stableKey = resolveStableRowKey(row)
    if (stableKey !== undefined) return stableKey

    // Object references survive pure reordering but change across page/filter results.
    // Primitive rows have no stable identity, so force conservative invalidation.
    return row !== null && typeof row === 'object' ? row : Symbol('unstable-row')
  })
)

const hasSameRowIdentitySet = (
  current: RowIdentityToken[],
  previous: RowIdentityToken[]
) => {
  if (current.length !== previous.length) return false
  const currentKeys = new Set(current)
  const previousKeys = new Set(previous)
  // Duplicate keys make row-to-cache ownership ambiguous, even when the unique
  // key set looks unchanged (for example [1, 1, 2] -> [1, 2, 2]).
  if (currentKeys.size !== current.length || previousKeys.size !== previous.length) return false
  return [...currentKeys].every(key => previousKeys.has(key))
}

/**
 * What the list is seen through: the nearest ancestor that scrolls it (a dialog or drawer body; one that cannot
 * scroll, such as an overflow-hidden card, does not count) or a fixed overlay, else the document. Its `top` is the
 * first visible line: the ancestor's top, or under the sticky page header for the document (the list's
 * scroll-margin-top: --table-sticky-top, 4rem by default).
 */
const viewOf = (el: HTMLElement) => {
  for (let node = el.parentElement; node && node !== document.body; node = node.parentElement) {
    const style = getComputedStyle(node)
    const scrolls = /auto|scroll|overlay/.test(style.overflowY) && node.scrollHeight > node.clientHeight
    if (scrolls || style.position === 'fixed') return { parent: node, top: () => node.getBoundingClientRect().top }
  }
  const header = parseFloat(getComputedStyle(el).scrollMarginTop) || 0
  return { parent: null, top: () => header }
}

/**
 * Back to the first row: the desktop table scrolls its own wrapper (and the virtualizer) to the top; when the list's
 * top has scrolled above the first visible line, it is brought back to it. A list that is already in view does not
 * move.
 */
function scrollToTop() {
  const wrapper = tableWrapperRef.value
  if (wrapper) {
    if (shouldVirtualize.value) rowVirtualizer.value.scrollToOffset(0)
    wrapper.scrollTop = 0
  }
  const list = wrapper ?? mobileRootRef.value
  if (!list) return
  const view = viewOf(list)
  const delta = list.getBoundingClientRect().top - view.top()
  if (delta >= 0) return
  if (view.parent) view.parent.scrollTop += delta
  else window.scrollBy(0, delta)
}

/**
 * Whether the first row is in view: the table's own scroll is at the top and the list's top is not above the first
 * visible line (below the sticky page header when the document scrolls). A live log follows new rows only then.
 */
function isAtTop() {
  const wrapper = tableWrapperRef.value
  if (wrapper && wrapper.scrollTop > 0) return false
  const list = wrapper ?? mobileRootRef.value
  if (!list) return true
  return list.getBoundingClientRect().top >= viewOf(list).top() - 1 // a scroll that stopped on a fraction still counts
}

// A new batch that shares no row with the old one is another page, filter or sort: start it from the top. A reload
// after a row action keeps most rows, and a passive update keeps all of them, so neither scrolls. Rows without a
// stable key cannot be compared across reloads and never scroll on their own.
const isStableKey = (key: RowIdentityToken) => typeof key === 'string' || typeof key === 'number'
const sharesNoRow = (current: RowIdentityToken[], previous: RowIdentityToken[]) => {
  if (!current.length || !previous.length || ![...current, ...previous].every(isStableKey)) return false
  const previousKeys = new Set(previous)
  return !current.some((key) => previousKeys.has(key))
}

watch(
  rowIdentityKeys,
  (current, previous) => {
    if (sharesNoRow(current, previous)) scrollToTop()
    if (hasSameRowIdentitySet(current, previous)) return

    // The virtualizer owns caches across option updates. A new page/filter result
    // must release detached rows and sizes, while pure reordering keeps them.
    rowVirtualizer.value.measureElement(null)
    rowVirtualizer.value.measure()
  },
  { flush: 'post' }
)

// 统一的渲染行列表:虚拟化开启时只取窗口内的行(需 measure 交给虚拟器测量),
// 关闭时取全部行(无需测量)。模板据此渲染,两种模式共用同一套单元格结构。
const renderRows = computed<Array<{ index: number; row: any; measure: boolean }>>(() => {
  const data = sortedData.value ?? []
  if (shouldVirtualize.value) {
    return virtualItems.value.map(vr => ({ index: vr.index, row: data[vr.index], measure: true }))
  }
  return data.map((row, index) => ({ index, row, measure: false }))
})

const hasActionsColumn = computed(() => {
  return props.columns.some(column => column.key === 'actions')
})

const hasSelectColumn = computed(() => {
  return props.columns.length > 0 && props.columns[0].key === 'select'
})

// 生成固定列的 CSS 类
/** The table layout freezes the built-in check box column with the first column (the explicit select column already is). */
const freezesBuiltinSelect = computed(() => props.layout === 'table' && props.selectable && props.stickyFirstColumn)
const builtinSelectStickyClass = computed(() => (freezesBuiltinSelect.value ? 'sticky-col sticky-col-left-first' : ''))

const getStickyColumnClass = (column: Column, index: number) => {
  const classes: string[] = []

  if (props.stickyFirstColumn) {
    // 如果第一列是勾选列，固定前两列（勾选+名称）
    if (hasSelectColumn.value || freezesBuiltinSelect.value) {
      if (freezesBuiltinSelect.value) {
        if (index === 0) classes.push('sticky-col sticky-col-left-second')
      } else if (index === 0) {
        classes.push('sticky-col sticky-col-left-first')
      } else if (index === 1) {
        classes.push('sticky-col sticky-col-left-second')
      }
    } else {
      // 否则只固定第一列
      if (index === 0) {
        classes.push('sticky-col sticky-col-left')
      }
    }
  }

  // 操作列固定（最后一列）
  if (props.stickyActionsColumn && column.key === 'actions') {
    classes.push('sticky-col sticky-col-right')
  }

  return classes.join(' ')
}

// 根据列数自适应调整内边距
const getAdaptivePaddingClass = () => {
  const columnCount = props.columns.length

  // 列数越多，内边距越小
  if (columnCount >= 10) {
    return 'px-2' // 8px
  } else if (columnCount >= 7) {
    return 'px-3' // 12px
  } else if (columnCount >= 5) {
    return 'px-4' // 16px
  } else {
    return 'px-6' // 24px (原始值)
  }
}

// 传给行组件的派生值保持引用稳定，避免滚动时整窗口的行因 props 变化而重渲染
const adaptivePaddingClass = computed(() => getAdaptivePaddingClass())
const stickyColumnClasses = computed(() => props.columns.map((column, index) => getStickyColumnClass(column, index)))

// Init + keep persisted sort state consistent with current columns
const didInitSort = ref(false)

onMounted(() => {
  const initial = resolveInitialSortState()
  applySortState(initial)
  didInitSort.value = true
})

watch(
  columnsSignature,
  () => {
    // If current sort key is no longer sortable/visible, fall back to default/persisted.
    const normalized = normalizeSortKey(sortKey.value)
    if (!sortKey.value) {
      const initial = resolveInitialSortState()
      applySortState(initial)
      return
    }

    if (!normalized) {
      const fallback = resolveInitialSortState()
      if (fallback) {
        applySortState(fallback)
      } else {
        sortKey.value = ''
        sortOrder.value = 'asc'
      }
    }
  },
  { flush: 'post' }
)

watch(
  [sortKey, sortOrder],
  ([nextKey, nextOrder]) => {
    if (!didInitSort.value) return
    if (!props.sortStorageKey) return
    const key = normalizeSortKey(nextKey)
    if (!key) return
    writePersistedSortState({ key, order: normalizeSortOrder(nextOrder) })
  },
  { flush: 'post' }
)

defineExpose({
  scrollToTop,
  isAtTop,
  virtualizer: rowVirtualizer,
  shouldVirtualize,
  sortedData,
  resolveRowKey,
  tableWrapperEl: tableWrapperRef,
})
</script>

<style scoped>
/* 表格横向滚动 */
.table-wrapper {
  scroll-margin-top: var(--table-sticky-top, 4rem);
  --select-col-width: 52px; /* 勾选列宽度：px-6 (24px*2) + checkbox (16px) */
  position: relative;
  overflow-x: auto;
  overflow-y: auto;
  flex: 1;
  min-height: 0;
  isolation: isolate;
}

/* 表头容器，确保在滚动时覆盖表体内容。颜色全部来自 styles/tokens.css 的语义变量 */
.table-wrapper .table-header {
  position: sticky;
  top: 0;
  z-index: 200;
  background-color: var(--color-bg-header);
}

/* 表体保持在表头下方 */
.table-body {
  position: relative;
  z-index: 0;
}

/* 行分隔线挂在每一行自身上，table/tbody 祖先上不放 divide-y / space-y：
   Tailwind v3 的这两类工具编译为 `X > :not([hidden]) ~ :not([hidden])`，祖先命中后
   Chromium 会在每次插入/移除行时重算整个 tbody 子树（数千节点），虚拟滚动每帧都触发。
   首行上边框同时充当表头分隔线，用较强的线色。 */
.table-wrapper :deep(.table-row-border) {
  border-top: 1px solid var(--color-line-row);
}

.table-wrapper :deep(tbody > .table-row-border:first-child) {
  border-top-color: var(--color-line-strong);
}

/* 所有表头单元格固定在顶部 */
.sticky-header-cell {
  position: sticky;
  top: 0;
  z-index: 210; /* 必须高于所有表体内容 */
  background-color: var(--color-bg-header);
}

/* Sticky 列基础样式（表体单元格由 DataTableRow 渲染，需 :deep 才能命中） */
.table-wrapper :deep(.sticky-col) {
  position: sticky;
  z-index: 20; /* 表体固定列 */
}

/* 单列固定（无勾选列时） */
.table-wrapper :deep(.sticky-col-left) {
  left: 0;
}

/* 双列固定（有勾选列时）：第一列（勾选） */
.table-wrapper :deep(.sticky-col-left-first) {
  left: 0;
}

/* 双列固定（有勾选列时）：第二列（名称） */
.table-wrapper :deep(.sticky-col-left-second) {
  left: var(--select-col-width);
}

/* 操作列固定 */
.table-wrapper :deep(.sticky-col-right) {
  right: 0;
}

/* 表头 sticky 列 - 需要比普通表头单元格更高的 z-index */
.table-wrapper .sticky-header-cell.sticky-col {
  z-index: 220; /* 高于普通表头单元格和表体固定列 */
}

/* 表体 sticky 列背景 */
.table-wrapper :deep(tbody .sticky-col) {
  background-color: var(--color-bg-surface);
}

/* hover 状态保持 */
.table-wrapper :deep(tbody tr:hover .sticky-col) {
  background-color: var(--color-bg-row-hover);
}

/* A ticked row (the table's own check boxes, or a page's row class "is-selected"): the selected background over the
   whole row, frozen cells included; under the pointer a step deeper, still apart from a hovered row (12.1-3). */
.table-wrapper :deep(tbody tr.is-selected),
.table-wrapper :deep(tbody tr.is-selected .sticky-col) {
  background-color: var(--color-bg-selected);
}
.table-wrapper :deep(tbody tr.is-selected:hover),
.table-wrapper :deep(tbody tr.is-selected:hover .sticky-col) {
  background-color: var(--color-bg-selected-hover);
}

/* 固定列的分隔线只在确实可以横向滚动时显示（规格 7b）：靠滚动区一侧 1px，表头与表体连成一条 */
.is-scrollable :deep(.sticky-col-left),
.is-scrollable :deep(.sticky-col-left-second) {
  box-shadow: inset -1px 0 0 var(--color-line-strong);
}

.is-scrollable :deep(.sticky-col-right) {
  box-shadow: inset 1px 0 0 var(--color-line-strong);
}
/* ---- The table layout (mobile list plan) ---- */
/* The built-in check box column is 44px (w-11): the first column freezes right after it. */
.table-wrapper.dt-builtin-select {
  --select-col-width: 44px;
}
/* A sortable header is one button across its cell: the button keeps the header's look, its ::after takes the whole
   cell (the th is sticky, so positioned), and keyboard focus rings the cell. */
.dt-sort {
  width: 100%;
  min-width: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  letter-spacing: inherit;
  text-align: inherit;
  text-transform: inherit;
  cursor: pointer;
}
.dt-sort::after {
  position: absolute;
  inset: 0;
  content: '';
}
.dt-sort:focus {
  outline: none;
}
.dt-sort:focus-visible::after {
  border-radius: var(--radius-sm);
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
/* Phones: the cells sit 12px from their edges whatever the column count; the frozen identity column (the first one,
   after any check box) is clamp(120px, 36vw, 160px) wide and cuts its lines with "…"; the frozen actions column is a
   44px square for the row's "⋯". */
@media (max-width: 767px) {
  .dt-layout-table {
    --table-identity-width: clamp(120px, 36vw, 160px);
  }
  .dt-layout-table :deep(th),
  .dt-layout-table :deep(td) {
    padding-inline: 12px;
  }
  .dt-layout-table :deep(.sticky-col-left),
  .dt-layout-table :deep(.sticky-col-left-second) {
    width: var(--table-identity-width);
    min-width: var(--table-identity-width);
    max-width: var(--table-identity-width);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .dt-layout-table :deep(.sticky-col-right) {
    width: 44px;
    min-width: 44px;
    max-width: 44px;
    padding-inline: 0;
    text-align: center;
  }
}
/* Touch (pointer: coarse): rows at least 48px, the header 40px, a check box's hit area 40px (the box itself keeps
   its size); layout still follows the width. */
@media (pointer: coarse) {
  .dt-layout-table :deep(tbody td) {
    height: 48px;
  }
  .dt-layout-table .sticky-header-cell {
    height: 40px;
  }
  /* The built-in check boxes sit in a label as big as their cell (44 x 48 in the body, 44 x 40 in the header). */
  .dt-layout-table :deep(.dt-check) {
    display: inline-flex;
    width: 44px;
    height: 48px;
    margin: -16px -12px;
    align-items: center;
    justify-content: center;
    cursor: pointer;
  }
  .dt-layout-table thead :deep(.dt-check) {
    height: 40px;
    margin: -12px;
  }
}
/* Reload progress: a 2px bar on the top edge of the table or card list in the primary colour. It never takes layout
   space and needs no script; on the cards it sits on the list's top edge and does not follow the page scroll. */
.table-progress {
  position: absolute;
  top: 0;
  right: 0;
  left: 0;
  pointer-events: none;
  overflow: hidden;
  height: 2px;
}
/* Desktop: the wrapper is the scroll container at 1024px and up; sticky keeps the bar on the visible top edge and
   across the visible width after scrolling either way. Its negative margin cancels its height. */
.table-progress--table {
  position: sticky;
  top: 0;
  left: 0;
  margin-bottom: -2px;
  z-index: 230; /* above the sticky header cells */
}
/* The page header the document scrolls under (--table-sticky-top, 4rem by default): scrollToTop() stops below it. */
.table-cards {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  align-items: start;
  gap: 8px;
  scroll-margin-top: var(--table-sticky-top, 4rem);
}
@media (min-width: 768px) {
  .table-cards {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
.table-cards__full {
  grid-column: 1 / -1;
}
.table-card {
  min-width: 0;
  border: 1px solid var(--color-line-strong);
  border-radius: var(--radius-lg);
  background: var(--color-bg-surface);
}
.table-card.is-selected {
  border-color: theme('colors.primary.400');
  background: var(--color-bg-selected);
}
.table-card__head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 12px 16px;
}
.table-card__title {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--color-text-primary);
}
/* A page's own select column takes only its own width, the leading column right after it: the head does not split in
   two (a utility class would lose to the rule above). */
.table-card__title--select {
  flex: none;
}
.table-card__end {
  flex: none;
  max-width: 50%;
  min-width: 0;
  color: var(--color-text-primary);
}
/* Label over value, two to a row; long text wraps. A value never clips: menus and tips that open from a cell would be
   cut at its edge (7j-1). */
.table-card__fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px 16px;
  margin: 0;
  padding: 12px 16px;
  border-top: 1px solid var(--color-line-row);
}
.table-card__field {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 2px;
}
.table-card__label {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
}
.table-card__value {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
}
.table-card__foot {
  display: flex;
  justify-content: flex-end;
  padding: 8px 12px;
  border-top: 1px solid var(--color-line-row);
}
.table-progress--cards {
  z-index: 10;
  margin: 0 !important; /* not part of the card stack's spacing */
  border-radius: 9999px;
}
.table-progress__bar {
  height: 100%;
  width: 100%;
  background: theme('colors.primary.500');
  transform-origin: left center;
  animation: table-progress-slide 1.1s linear infinite;
}
.dark .table-progress__bar {
  background: theme('colors.primary.400');
}
/* With reduced motion the animation is cut short and the bar rests as a full-width line. */
@keyframes table-progress-slide {
  from {
    transform: translateX(-100%) scaleX(0.35);
  }
  to {
    transform: translateX(100%) scaleX(0.35);
  }
}
/* Touch: the sort direction button (36px, 34px inside its border, which an absolute area does not count) reaches 40px
   each way with an area sized on 40px, its look unchanged (7n-3, 7q-8). The area grows down and to the sides, never
   up: the button can sit at the top edge of a clipping container (the accounts table), which would cut 2px off it.
   From the top of the inside of the border it reaches 40px down, 5px past the button into the 8px gap above the first
   card; sideways 2px past each edge, into the 8px gaps of its row. */
@media (pointer: coarse) {
  .table-cards__sort-order {
    position: relative;
  }
  .table-cards__sort-order::after {
    position: absolute;
    top: 0;
    left: 50%;
    width: max(100%, 40px);
    height: max(100%, 40px);
    transform: translateX(-50%);
    content: '';
  }
}
</style>

<style>
/* ==========================================================================
   终极悬浮滚动条防丢器 (Sledgehammer Override)
   绕过 style.css 中 `* { scrollbar-color: transparent }` 的全局悬停隐身诅咒！
   ========================================================================== */

/* 1. 废除全局针对所有元素的 scrollbar-width 设定，拿回 Chrome/Safari 下 Webkit 滚动条规则的控制权！ */
.table-wrapper {
  scrollbar-width: auto !important; /* 阻止 Chrome 121 退化到原生 Mac 闪隐滚动条 */
}

/* 2. 重写 Webkit 滚动层，全部加上 !important 强制覆盖透明悬停陷阱 */
.table-wrapper::-webkit-scrollbar {
  height: 12px !important;
  width: 12px !important;
  display: block !important;
  background-color: transparent !important;
}

.table-wrapper::-webkit-scrollbar-track {
  background-color: rgba(0, 0, 0, 0.03) !important;
  border-radius: 6px !important;
  margin: 0 4px !important;
}
.dark .table-wrapper::-webkit-scrollbar-track {
  background-color: rgba(255, 255, 255, 0.05) !important;
}

/* 常驻、不透明的滑块，无视鼠标是否 hover 都在那！ */
.table-wrapper::-webkit-scrollbar-thumb {
  background-color: rgba(107, 114, 128, 0.75) !important; 
  border-radius: 6px !important;
  border: 2px solid transparent !important;
  background-clip: padding-box !important;
  -webkit-appearance: none !important;
}
.table-wrapper::-webkit-scrollbar-thumb:hover {
  background-color: rgba(75, 85, 99, 0.9) !important;
}

.dark .table-wrapper::-webkit-scrollbar-thumb {
  background-color: rgba(156, 163, 175, 0.75) !important;
}
.dark .table-wrapper::-webkit-scrollbar-thumb:hover {
  background-color: rgba(209, 213, 219, 0.9) !important;
}

/* 3. 仅给真正的 Firefox 留的后路 */
@supports (-moz-appearance:none) {
  .table-wrapper {
    scrollbar-width: thin !important;
    scrollbar-color: rgba(156, 163, 175, 0.5) rgba(0, 0, 0, 0.03) !important;
  }
  .dark .table-wrapper {
    scrollbar-color: rgba(75, 85, 99, 0.5) rgba(255, 255, 255, 0.05) !important;
  }
}
</style>
