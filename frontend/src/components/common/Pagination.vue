<template>
  <div
    class="flex items-center justify-between border-t border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800 sm:px-6"
  >
    <!-- Below 1024px (spec 8.6): page size, then previous, page / pages and next on one row -->
    <div class="flex min-w-0 flex-1 items-center justify-between gap-3 lg:hidden" data-test="pagination-compact">
      <div v-if="showPageSizeSelector" class="page-size-select w-20">
        <Select
          :model-value="pageSize"
          :options="pageSizeSelectOptions"
          :aria-label="t('pagination.perPage')"
          @update:model-value="handlePageSizeChange"
        />
      </div>
      <div class="ml-auto flex items-center gap-1">
        <button
          type="button"
          class="inline-flex h-10 w-10 items-center justify-center rounded-md text-gray-600 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:text-gray-300 dark:hover:bg-dark-700"
          :disabled="page === 1"
          :aria-label="t('pagination.previous')"
          @click="goToPage(page - 1)"
        >
          <Icon name="chevronLeft" size="md" />
        </button>
        <span class="whitespace-nowrap px-1 text-sm text-gray-700 dark:text-gray-300" data-test="pagination-page">
          {{ totalExact ? t('pagination.pageOf', { page, total: totalPages }) : t('pagination.pageOnly', { page }) }}
        </span>
        <button
          type="button"
          class="inline-flex h-10 w-10 items-center justify-center rounded-md text-gray-600 hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:text-gray-300 dark:hover:bg-dark-700"
          :disabled="page === totalPages"
          :aria-label="t('pagination.next')"
          @click="goToPage(page + 1)"
        >
          <Icon name="chevronRight" size="md" />
        </button>
      </div>
    </div>

    <div ref="wideRef" class="pager-wide hidden min-w-0 lg:flex lg:flex-1 lg:items-center lg:justify-between" :class="{ 'pager-wide--wrap': wrapRow }">
      <!-- Desktop pagination info -->
      <div class="flex items-center space-x-3">
        <!-- "第 1–20 条，共 74 条 · 每页 [20]" (mobile list plan, copy); "第 1–20 条" when the total is not exact -->
        <p class="whitespace-nowrap text-sm tabular-nums text-gray-700 dark:text-gray-300" data-test="pagination-range">
          {{
            totalExact
              ? t('pagination.range', { from: formatCount(fromItem), to: formatCount(toItem), total: formatCount(total) })
              : t('pagination.rangeOpen', { from: formatCount(fromItem), to: formatCount(toItem) })
          }}
        </p>

        <!-- Page size selector -->
        <div v-if="showPageSizeSelector" class="flex items-center space-x-2">
          <span class="whitespace-nowrap text-sm text-gray-700 dark:text-gray-300"
            ><span aria-hidden="true">· </span>{{ t('pagination.perPage') }}</span
          >
          <div class="page-size-select w-20 shrink-0">
            <Select
              :model-value="pageSize"
              :options="pageSizeSelectOptions"
              :aria-label="t('pagination.perPage')"
              @update:model-value="handlePageSizeChange"
            />
          </div>
        </div>

        <div v-if="showJump" class="flex items-center space-x-2">
          <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('pagination.jumpTo') }}</span>
          <input
            v-model="jumpPage"
            type="number"
            min="1"
            :max="totalPages"
            class="pager-jump input w-20 text-sm"
            :placeholder="t('pagination.jumpPlaceholder')"
            @keyup.enter="submitJump"
          />
          <button type="button" class="pager-jump btn btn-ghost btn-sm" @click="submitJump">
            {{ t('pagination.jumpAction') }}
          </button>
        </div>
      </div>

      <!-- Desktop pagination buttons -->
      <nav
        class="pager-nav relative z-0 inline-flex -space-x-px rounded-md shadow-sm"
        aria-label="Pagination"
      >
        <!-- Previous button -->
        <button
          @click="goToPage(page - 1)"
          :disabled="page === 1"
          class="pager-btn relative inline-flex items-center rounded-l-md border border-gray-300 bg-white px-2 py-2 text-sm font-medium text-gray-500 hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-400 dark:hover:bg-dark-600"
          :aria-label="t('pagination.previous')"
        >
          <Icon name="chevronLeft" size="md" />
        </button>

        <!-- Page numbers -->
        <button
          v-for="(pageNum, index) in visiblePages"
          :key="`${pageNum}-${index}`"
          @click="typeof pageNum === 'number' && goToPage(pageNum)"
          :disabled="typeof pageNum !== 'number'"
          :class="[
            'pager-btn relative inline-flex items-center border px-4 py-2 text-sm font-medium',
            pageNum === page
              ? 'z-10 border-primary-500 bg-primary-50 text-primary-600 dark:bg-primary-900/30 dark:text-primary-400'
              : 'border-gray-300 bg-white text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-300 dark:hover:bg-dark-600',
            typeof pageNum !== 'number' && 'cursor-default'
          ]"
          :aria-label="
            typeof pageNum === 'number' ? t('pagination.goToPage', { page: pageNum }) : undefined
          "
          :aria-current="pageNum === page ? 'page' : undefined"
        >
          {{ pageNum }}
        </button>

        <!-- Next button -->
        <button
          @click="goToPage(page + 1)"
          :disabled="page === totalPages"
          class="pager-btn relative inline-flex items-center rounded-r-md border border-gray-300 bg-white px-2 py-2 text-sm font-medium text-gray-500 hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:bg-dark-700 dark:text-gray-400 dark:hover:bg-dark-600"
          :aria-label="t('pagination.next')"
        >
          <Icon name="chevronRight" size="md" />
        </button>
      </nav>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from './Select.vue'
import { getConfiguredTablePageSizeOptions, normalizeTablePageSize } from '@/utils/tablePreferences'
import { setPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useMediaQuery } from '@vueuse/core'

const i18n = useI18n()
const { t } = i18n
/** Counts with thousands separators ("1,234"), in the page's language. */
const formatCount = (value: number) => value.toLocaleString(i18n.locale?.value)

interface Props {
  total: number
  page: number
  pageSize: number
  pageSizeOptions?: number[]
  showPageSizeSelector?: boolean
  showJump?: boolean
  /**
   * Whether `total` is the exact count (default). False for a list that counts only as far as it needs: its total is
   * "the end of this page, plus one" when there is a next page. Then the range reads "第 1–20 条" / "1–20" and the
   * narrow line "第 2 页" / "Page 2", with no total; the page buttons and "next" still go by `total`.
   */
  totalExact?: boolean
}

interface Emits {
  (e: 'update:page', page: number): void
  (e: 'update:pageSize', pageSize: number): void
}

const props = withDefaults(defineProps<Props>(), {
  pageSizeOptions: () => getConfiguredTablePageSizeOptions(),
  showPageSizeSelector: true,
  showJump: false,
  totalExact: true
})

const emit = defineEmits<Emits>()

const totalPages = computed(() => Math.ceil(props.total / props.pageSize))

const fromItem = computed(() => {
  if (props.total === 0) return 0
  return (props.page - 1) * props.pageSize + 1
})

const toItem = computed(() => {
  const to = props.page * props.pageSize
  return to > props.total ? props.total : to
})

const pageSizeSelectOptions = computed(() => {
  const options = Array.from(
    new Set([
      ...getConfiguredTablePageSizeOptions(),
      normalizeTablePageSize(props.pageSize)
    ])
  ).sort((a, b) => a - b)

  return options.map((size) => ({
    value: size,
    label: String(size)
  }))
})

const jumpPage = ref('')

/**
 * Pages shown either side of the current one in the wide row: 2, fewer where the row is too narrow for them (an iPad
 * at 1024px with thousands of pages), down to 0 (first, current, last).
 */
const siblings = ref(2)
/** Even with none either side the pages do not fit beside the range: they take a line of their own. */
const wrapRow = ref(false)
const wideRef = ref<HTMLElement | null>(null)
/** The row runs past its edge: its range and page size keep one line and their width, so a tight row overflows. */
const tooTight = (row: HTMLElement) => row.scrollWidth > row.clientWidth + 1

const visiblePages = computed(() => {
  const pages: (number | string)[] = []
  const around = siblings.value
  const maxVisible = around * 2 + 3
  const total = totalPages.value

  if (total <= maxVisible) {
    // Show all pages if total is small
    for (let i = 1; i <= total; i++) {
      pages.push(i)
    }
  } else {
    // Always show first page
    pages.push(1)

    const start = Math.max(2, props.page - around)
    const end = Math.min(total - 1, props.page + around)

    // Add ellipsis before if needed
    if (start > 2) {
      pages.push('...')
    }

    // Add middle pages
    for (let i = start; i <= end; i++) {
      pages.push(i)
    }

    // Add ellipsis after if needed
    if (end < total - 1) {
      pages.push('...')
    }

    // Always show last page
    pages.push(total)
  }

  return pages
})

/** Shows as many pages around the current one as the wide row holds without running past its edge. */
let fitting = false
let fitAgain = false
async function fitPages() {
  if (fitting) {
    fitAgain = true
    return
  }
  fitting = true
  try {
    do {
      fitAgain = false
      siblings.value = 2
      wrapRow.value = false
      await nextTick()
      const row = wideRef.value
      // Hidden below 1024px (the narrow row shows no page buttons).
      if (!row || !row.clientWidth) continue
      while (siblings.value > 0 && tooTight(row)) {
        siblings.value--
        await nextTick()
      }
      if (tooTight(row)) wrapRow.value = true
    } while (fitAgain)
  } finally {
    fitting = false
  }
}
// Touch sizes and wraps the row differently (the styles' own query): a change of pointer refits too.
const coarse = useMediaQuery('(pointer: coarse)')
watch(() => [props.page, props.total, props.pageSize, coarse.value], () => { void fitPages() }, { flush: 'post' })
let observer: ResizeObserver | null = null
let lastWidth = -1
onMounted(() => {
  void fitPages()
  if (typeof ResizeObserver === 'undefined' || !wideRef.value?.parentElement) return
  // Its width only: fewer pages can change the row's height (a line of its own on touch), which must not refit.
  observer = new ResizeObserver(([entry]) => {
    const width = Math.round(entry.contentRect.width)
    if (width === lastWidth) return
    lastWidth = width
    void fitPages()
  })
  observer.observe(wideRef.value.parentElement)
})
onBeforeUnmount(() => observer?.disconnect())

const goToPage = (newPage: number) => {
  if (newPage >= 1 && newPage <= totalPages.value && newPage !== props.page) {
    emit('update:page', newPage)
  }
}

const handlePageSizeChange = (value: string | number | boolean | null) => {
  if (value === null || typeof value === 'boolean') return
  const newPageSize = normalizeTablePageSize(typeof value === 'string' ? parseInt(value, 10) : value)
  setPersistedPageSize(newPageSize)
  emit('update:pageSize', newPageSize)
}

const submitJump = () => {
  const value = String(jumpPage.value).trim()
  if (!value) return
  const pageNum = Number.parseInt(value, 10)
  if (Number.isNaN(pageNum)) return
  const nextPage = Math.min(Math.max(pageNum, 1), totalPages.value)
  jumpPage.value = ''
  goToPage(nextPage)
}
</script>

<style scoped>
.page-size-select :deep(.select-trigger) {
  @apply px-3 py-1.5 text-sm;
}
/* Too narrow for the pages even with none either side of the current one (1024px, thousands of pages): they take a
   line of their own, on the right. Touch does so first (below). */
.pager-wide--wrap {
  flex-wrap: wrap;
  gap: 8px 16px;
}
.pager-wide--wrap > .pager-nav {
  margin-left: auto;
}
/* Touch (pointer: coarse, mobile list plan): every control 40px to hit and 8px apart, so the page buttons part from
   their joined group; the jump field takes 16px type (no zoom on focus). Where a long run of pages does not fit beside
   the range (an iPad at 1024px), the buttons take a line of their own, on the right. A mouse keeps the joined 38px
   buttons on one line. */
@media (pointer: coarse) {
  .pager-wide {
    flex-wrap: wrap;
    gap: 8px 16px;
  }
  .pager-wide > .pager-nav {
    margin-left: auto;
  }
  .page-size-select :deep(.select-trigger) {
    min-height: 40px;
  }
  .pager-nav {
    gap: 8px;
    box-shadow: none;
  }
  .pager-nav > .pager-btn:not([hidden]) {
    min-width: 40px;
    min-height: 40px;
    justify-content: center;
    margin: 0;
    border-radius: var(--radius-md);
  }
  .pager-jump {
    min-height: 40px;
  }
  input.pager-jump {
    font-size: 16px;
  }
}
</style>
