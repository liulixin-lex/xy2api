<template>
  <div
    ref="tableWrapperRef"
    class="table-wrapper dt-layout-table"
    :class="{
      'actions-expanded': actionsExpanded,
      'is-scrollable': isScrollable,
      'dt-builtin-select': selectable
    }"
    :style="freezePadding"
    :tabindex="scrollLabel && isScrollable ? 0 : undefined"
    :role="scrollLabel ? 'region' : undefined"
    :aria-label="scrollLabel || undefined"
    @focusin="revealFocused"
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
            :class="['dt-select sticky-header-cell w-11 min-w-11 px-3 py-3 text-center', builtinSelectStickyClass]"
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
          <td v-if="selectable" :class="['dt-select w-11 min-w-11 px-3 py-4', builtinSelectStickyClass]">
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
import { computed, inject, ref, onMounted, onUnmounted, watch, nextTick, type ComponentPublicInstance } from 'vue'
import { useVirtualizer, observeElementRect as observeElementRectDefault, type Virtualizer } from '@tanstack/vue-virtual'
import { useI18n } from 'vue-i18n'
import type { Column } from './types'
import Icon from '@/components/icons/Icon.vue'
import DataTableRow from './DataTableRow'
import { TABLE_REGION_KEY } from '@/components/layout/tableRegion'

const { t } = useI18n()

const emit = defineEmits<{
  sort: [key: string, order: 'asc' | 'desc']
  rowClick: [row: any]
  'update:selectedKeys': [keys: Array<string | number>]
  selectionChange: [keys: Array<string | number>]
}>()

// 表格容器引用
const tableWrapperRef = ref<HTMLElement | null>(null)
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

/**
 * What the frozen parts cover of the scroll area: the left frozen columns, the right one and the sticky header. The
 * wrapper's scroll padding keeps them out of the area a focused cell is scrolled into, so keyboard focus never sits
 * under a frozen column or the header (mobile list plan; Astra review #3).
 */
const freezeInsets = ref({ left: 0, right: 0, top: 0 })
const freezePadding = computed(() => ({
  scrollPaddingLeft: `${freezeInsets.value.left}px`,
  scrollPaddingRight: `${freezeInsets.value.right}px`,
  scrollPaddingTop: `${freezeInsets.value.top}px`
}))
const measureFreeze = (wrapper: HTMLElement) => {
  const firstRow = wrapper.querySelector<HTMLElement>(':scope > table > thead > tr')
  const width = (el: Element) => el.getBoundingClientRect().width
  const left = Array.from(firstRow?.querySelectorAll('.sticky-col-left, .sticky-col-left-first, .sticky-col-left-second') ?? []).reduce((sum, th) => sum + width(th), 0)
  const rightCell = firstRow?.querySelector('.sticky-col-right')
  const next = {
    left: Math.round(left),
    right: Math.round(rightCell ? width(rightCell) : 0),
    top: Math.round(wrapper.querySelector<HTMLElement>(':scope > table > thead')?.getBoundingClientRect().height ?? 0)
  }
  const now = freezeInsets.value
  if (now.left !== next.left || now.right !== next.right || now.top !== next.top) freezeInsets.value = next
}

/**
 * Keyboard focus on a control in an unfrozen cell: when the browser's own scroll leaves part of it under a frozen
 * column or the header, the wrapper scrolls by the rest, so the ring and the control are whole in view.
 */
const REVEAL_GAP = 4
function revealFocused(event: FocusEvent) {
  const wrapper = tableWrapperRef.value
  const target = event.target
  if (!wrapper || !(target instanceof HTMLElement) || !target.matches(':focus-visible')) return
  const cell = target.closest('td, th')
  if (!cell || cell.classList.contains('sticky-col') || cell.classList.contains('sticky-header-cell')) return
  const box = wrapper.getBoundingClientRect()
  const rect = target.getBoundingClientRect()
  const insets = freezeInsets.value
  const left = box.left + insets.left
  const right = box.left + wrapper.clientWidth - insets.right
  if (rect.left < left) wrapper.scrollLeft -= left - rect.left + REVEAL_GAP
  else if (rect.right > right) wrapper.scrollLeft += Math.min(rect.right - right + REVEAL_GAP, rect.left - left)
  const top = box.top + insets.top
  const bottom = box.top + wrapper.clientHeight
  if (rect.top < top) wrapper.scrollTop -= top - rect.top + REVEAL_GAP
  else if (rect.bottom > bottom) wrapper.scrollTop += rect.bottom - bottom + REVEAL_GAP
}

// 检查是否可滚动
const checkScrollable = () => {
  if (tableWrapperRef.value) {
    isScrollable.value = tableWrapperRef.value.scrollWidth > tableWrapperRef.value.clientWidth
    measureFreeze(tableWrapperRef.value)
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

const detachTableTracking = () => {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (resizeHandler) {
    window.removeEventListener('resize', resizeHandler)
    resizeHandler = null
  }
}

/**
 * The sticky header's own height (both rows of a grouped header) as --table-head-h on the scroll box, which keeps
 * that much at its top out of reach of focus scrolling and scrollIntoView (scroll-padding-top). Read when the header
 * resizes, never per frame.
 */
const measureHead = () => {
  const wrapper = tableWrapperRef.value
  const head = wrapper?.querySelector<HTMLElement>(':scope > table > thead')
  if (wrapper && head) wrapper.style.setProperty('--table-head-h', `${head.getBoundingClientRect().height}px`)
}

const attachTableTracking = () => {
  checkScrollable()
  checkActionsColumnWidth()
  measureHead()
  if (tableWrapperRef.value && typeof ResizeObserver !== 'undefined') {
    const wrapper = tableWrapperRef.value
    const head = wrapper.querySelector(':scope > table > thead')
    resizeObserver = new ResizeObserver((entries) => {
      checkScrollable()
      // Only the wrapper's own size changes the room of the actions column; a change of the table alone just
      // re-checks the sideways scroll (spec 7g-4).
      if (entries.some((entry) => entry.target === wrapper)) checkActionsColumnWidth()
      if (head && entries.some((entry) => entry.target === head)) measureHead()
    })
    resizeObserver.observe(wrapper)
    // The table too: a page may narrow its columns after the wrapper's resize (the accounts page recomputes its column
    // widths from the new width), and only the table's own size change tells that it no longer scrolls sideways.
    const table = wrapper.querySelector(':scope > table')
    if (table) resizeObserver.observe(table)
    if (head) resizeObserver.observe(head)
  } else {
    // 降级方案：不支持 ResizeObserver 时使用 window resize
    resizeHandler = () => {
      checkScrollable()
      checkActionsColumnWidth()
      measureHead()
    }
    window.addEventListener('resize', resizeHandler)
  }
}

onUnmounted(() => {
  detachTableTracking()
})

interface Props {
  /**
   * The table's name for a screen reader (mobile list plan): the scroll area is a named region, and while it scrolls
   * sideways it takes keyboard focus, so the arrow keys can scroll it.
   */
  scrollLabel?: string
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
   * The page's own sort (server-side sorting, mobile list plan): the headers show it and the table keeps no sort of its
   * own, so hiding the sorted column (another width's columns) changes no request, no stored preference and no other
   * header; the column shows its arrow again when it is back. A header click still emits `sort` and stores the choice.
   */
  sort?: { key: string; order: 'asc' | 'desc' } | null
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
  /** Emit 'rowClick' on a row click and show pointer cursor (interactive cells should @click.stop) */
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
  /** Accessible label for a row selection checkbox. */
  selectionLabel?: string | ((row: any) => string)
  /**
   * Extra classes for a row (for example dimming a row that is transitioning or no longer matches the filter). A row
   * calls it in its own render, so only rows whose answer changes re-render. Pass a
   * stable function defined once in setup: an inline arrow in the template is a new function on every render of the
   * parent and re-renders every row.
   */
  rowClass?: (row: any, index: number) => unknown
}

const props = withDefaults(defineProps<Props>(), {
  sort: undefined,
  loading: false,
  stickyFirstColumn: true,
  stickyActionsColumn: true,
  expandableActions: true,
  defaultSortOrder: 'asc',
  serverSideSort: false,
  selectable: false,
  selectedKeys: () => []
})

/**
 * Where rows may be windowed: only inside a bounded table region, at every width (TablePageLayout's one decision,
 * useTableRegion); a table outside it, or while the page scrolls, renders every row.
 */
const tableRegion = inject(TABLE_REGION_KEY, null)
const windowedHere = computed(() => tableRegion?.value === true)

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

// The table's size tracking (sideways scroll, the actions column's room), from its first render.
onMounted(attachTableTracking)

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

  // A page that owns the sort only hears the choice: the arrows and aria-sort follow the sort it passes back, and the
  // choice is stored once it does (it may wait, or refuse).
  if (pageOwnsSort.value) {
    requestedSort = { key, order: newOrder }
    emit('sort', key, newOrder)
    return
  }
  // A header click is the user's choice: the one place the sort is stored.
  writePersistedSortState({ key, order: newOrder })
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
// 是否启用虚拟化:仅在有界表格区内且行数超过阈值时开启。小列表全量渲染,彻底绕开虚拟器的
// 估算/测量/滚动补偿链路,消除可变行高导致的滚动抖动。
const shouldVirtualize = computed(() =>
  windowedHere.value && (sortedData.value?.length ?? 0) > (props.virtualizeThreshold ?? 100)
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
  const list = wrapper
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
  const list = wrapper
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


const hasSelectColumn = computed(() => {
  return props.columns.length > 0 && props.columns[0].key === 'select'
})

// 生成固定列的 CSS 类
/** The table layout freezes the built-in check box column with the first column (the explicit select column already is). */
const freezesBuiltinSelect = computed(() => props.selectable && props.stickyFirstColumn)
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

// Init + keep the sort consistent with the current columns (a sort the table owns)
/** The page owns the sort: the headers follow it, whatever columns are shown. */
const pageOwnsSort = computed(() => props.sort !== undefined)
/** The header click a page that owns the sort has not answered yet: stored when the page's sort becomes it. */
let requestedSort: PersistedSortState | null = null
// Watched by value (key and order): a page that renders again with an equal new object neither changes the headers
// nor drops a click still waiting for its answer (Astra second re-review #2). No `sort` (the table's own) and `null`
// (the page's, with no order) are two values, so a page taking over with no order clears the arrow (third re-review).
watch(
  () => (props.sort === undefined ? '\u0001' : props.sort ? `${props.sort.key}\u0000${props.sort.order}` : ''),
  () => {
    if (!pageOwnsSort.value) return
    const sort = props.sort
    sortKey.value = sort?.key ?? ''
    sortOrder.value = sort?.order ?? 'asc'
    if (requestedSort && sort?.key === requestedSort.key && sort.order === requestedSort.order) writePersistedSortState(requestedSort)
    requestedSort = null
  },
  { immediate: true }
)

onMounted(() => {
  if (!pageOwnsSort.value) applySortState(resolveInitialSortState())
})

watch(
  columnsSignature,
  () => {
    // A column shown or hidden changes nothing in a sort the page owns (only whether its header shows the arrow).
    if (pageOwnsSort.value) return
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
/* 表格横向滚动。scroll-padding-top 让出吸顶表头（--table-head-h：表头实测高度，分组表头为两行之和，未测到前用
   40px）：聚焦滚动和 scrollIntoView 不把行停在表头下面 */
.table-wrapper {
  scroll-margin-top: var(--table-sticky-top, 4rem);
  scroll-padding-top: var(--table-head-h, var(--header-height, 40px));
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
/* The scroll area itself takes keyboard focus while it scrolls sideways (scrollLabel): its ring is drawn inside. */
.table-wrapper:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
/* ---- The table layout (mobile list plan) ---- */
/* The built-in check box column is exactly 44px, its border box whatever padding the page gives cells (TablePageLayout
   has 20px): the first column freezes right after it, at --select-col-width, the two meeting without overlap. The
   check box sits in a label as big as the cell. */
.table-wrapper.dt-builtin-select {
  --select-col-width: 44px;
}
.table-wrapper.dt-builtin-select :deep(.dt-select) {
  box-sizing: border-box;
  width: 44px;
  min-width: 44px;
  max-width: 44px;
  padding: 0;
}
.table-wrapper.dt-builtin-select :deep(.dt-check) {
  display: flex;
  width: 44px;
  min-height: 40px;
  align-items: center;
  justify-content: center;
  cursor: pointer;
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
  /* The built-in check boxes' labels: 48px high in the body, 40px in the header. */
  .table-wrapper.dt-builtin-select :deep(tbody .dt-check) {
    min-height: 48px;
  }
}
/* Reload progress: a 2px bar on the top edge of the table in the primary colour. It never takes layout space and
   needs no script. */
.table-progress {
  position: absolute;
  top: 0;
  right: 0;
  left: 0;
  pointer-events: none;
  overflow: hidden;
  height: 2px;
}
/* Sticky keeps the bar on the visible top edge and across the visible width after scrolling either way. Its negative
   margin cancels its height. */
.table-progress--table {
  position: sticky;
  top: 0;
  left: 0;
  margin-bottom: -2px;
  z-index: 230; /* above the sticky header cells */
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

/* The corner where the two scrollbars meet takes the table's surface, not the browser's white (both themes). */
.table-wrapper::-webkit-scrollbar-corner {
  background-color: var(--color-bg-surface) !important;
}

/* Touch (mobile list plan): a finger scrolls the table itself, so the bars are 6px hints, not 12px targets. */
@media (pointer: coarse) {
  .table-wrapper {
    scrollbar-width: thin !important;
  }
  .table-wrapper::-webkit-scrollbar {
    width: 6px !important;
    height: 6px !important;
  }
  .table-wrapper::-webkit-scrollbar-thumb {
    border-width: 1px !important;
  }
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
