<template>
  <div ref="rootRef" class="tb-root" data-test="accounts-toolbar">
    <TableToolbar class="tb" compact filter-sheet :filter-count="tags.length" @clear-filters="clearAll">
      <template #search>
      <label class="table-toolbar__search-box">
        <span class="sr-only">{{ t('admin.accounts.table.searchPlaceholder') }}</span>
        <Icon name="search" size="sm" class="table-toolbar__search-icon" />
        <input
          ref="searchRef"
          class="table-toolbar__input"
          type="search"
          autocomplete="off"
          :value="search"
          :placeholder="t('admin.accounts.table.searchPlaceholder')"
          data-test="accounts-search"
          @input="emit('update:search', ($event.target as HTMLInputElement).value)"
        />
      </label>
      </template>
      <template v-if="!phone" #filters>
      <div ref="filtersRef" class="tb-filters">
        <!-- The shared Select's sm (12.6): the filter's name until a value is chosen, "all" first in its panel. -->
        <template v-for="key in DIRECT" :key="key">
          <Select
            v-if="!foldedSelects.includes(key)"
            size="sm"
            class="tb-sel"
            :model-value="filters[FIELD[key]] ?? ''"
            :options="selectOptions(key)"
            :aria-label="label(key)"
            :data-filter="key"
            @update:model-value="setFilter(key, String($event ?? ''))"
          >
            <template #selected="{ option }">{{ option && option.value !== '' ? option.label : label(key) }}</template>
          </Select>
        </template>
        <button
          ref="moreRef"
          type="button"
          class="table-toolbar__btn table-toolbar__btn--secondary"
          :aria-expanded="menuOpen ? 'true' : 'false'"
          aria-haspopup="true"
          :aria-label="folded.length ? t('admin.accounts.table.folded', { n: folded.length }) : undefined"
          v-tip="folded.length ? t('admin.accounts.table.folded', { n: folded.length }) : ''"
          data-test="more-filters"
          @click="menuOpen = !menuOpen"
        >
          <Icon name="filter" size="sm" />{{ t('admin.accounts.table.moreFilters') }}
          <span v-if="folded.length" class="tb-count" data-test="more-filters-count">{{ folded.length }}</span>
        </button>
      </div>
      <div ref="tagsRef" class="tb-tags" data-test="filter-tags">
        <span v-for="tag in tags" :key="tag.key" class="tb-tag" :hidden="folded.includes(tag.key)" :data-key="tag.key">
          <span class="tb-tag__k">{{ label(tag.key) }}：</span>
          <StatusDot v-if="tag.key === 'quality'" :tone="QUALITY_TONES[tag.value] ?? 'ok'" class="tb-tag__dot" />
          <span class="tb-tag__v">{{ tag.text }}</span>
          <button type="button" class="tb-tag__x" :aria-label="t('admin.accounts.table.removeFilter', { label: label(tag.key) })" @click="setFilter(tag.key, '')">
            <Icon name="x" size="xs" />
          </button>
        </span>
      </div>
      </template>

      <!-- A phone (mobile list plan): every filter in the bottom sheet, applied at once; the chosen ones as tags under
           the search; the count, the sort when its column is hidden, the live state and the tools on one row. -->
      <template v-if="phone" #sheet>
        <div v-for="key in sheetKeys" :key="key" class="tb-sheet__field">
          <span :id="`tb-sheet-${key}`" class="tb-sheet__k">{{ label(key) }}</span>
          <Select
            class="tb-sel--full"
            :model-value="filters[FIELD[key]] ?? ''"
            :options="selectOptions(key)"
            :aria-label="label(key)"
            :data-filter="key"
            @update:model-value="setFilter(key, String($event ?? ''))"
          />
        </div>
      </template>
      <template v-if="phone" #tags>
        <span v-for="tag in tags" :key="tag.key" class="tb-tag" :data-key="tag.key">
          <span class="tb-tag__k">{{ label(tag.key) }}：</span>
          <StatusDot v-if="tag.key === 'quality'" :tone="QUALITY_TONES[tag.value] ?? 'ok'" class="tb-tag__dot" />
          <span class="tb-tag__v">{{ tag.text }}</span>
          <button type="button" class="tb-tag__x" :aria-label="t('admin.accounts.table.removeFilter', { label: label(tag.key) })" @click="setFilter(tag.key, '')">
            <Icon name="x" size="xs" />
          </button>
        </span>
      </template>
      <template v-if="phone" #info>
        <span data-test="accounts-count">{{ t('common.table.count', { n: (total ?? 0).toLocaleString() }) }}</span>
        <template v-if="sortHint">
          <span class="table-toolbar__sep" aria-hidden="true">·</span>
          <TableSortHint :label="sortHint.label" :order="sortHint.order" />
        </template>
      </template>

      <template #status>
      <!-- Below 1024px only (a tablet): from 1024px, as before the mobile list plan, the toolbar has no sort hint. -->
      <TableSortHint v-if="!phone && !desktop && sortHint" class="tb-sort" :label="sortHint.label" :order="sortHint.order" />
      <LiveStatusIndicator :status="liveStatus" :pause="pause" :resume="resume" :detail="t('admin.accounts.table.liveDetail')" />
      </template>
      <template #actions>
      <button
        type="button"
        class="table-toolbar__icon-btn table-toolbar__icon"
        :aria-label="t('admin.accounts.table.refresh')"
        v-tip="t('admin.accounts.table.refreshTip')"
        data-test="accounts-refresh"
        @click="emit('refresh')"
      >
        <Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />
      </button>
      <button
        v-if="phone"
        type="button"
        class="table-toolbar__icon-btn table-toolbar__icon"
        aria-haspopup="dialog"
        :aria-label="t('common.table.columnsTitle')"
        data-test="accounts-columns"
        @click="emit('columns')"
      >
        <Icon name="grid" size="sm" />
      </button>
      <slot name="more-actions" />
      </template>
      <template v-if="canCreate" #create>
      <button type="button" class="table-toolbar__btn table-toolbar__btn--primary" :aria-label="t('admin.accounts.createAccount')" data-test="accounts-create" @click="emit('create')">
        <Icon name="plus" size="sm" /><span>{{ phone ? t('admin.accounts.table.createShort') : t('admin.accounts.createAccount') }}</span>
      </button>
      </template>
    </TableToolbar>

    <div v-if="menuOpen && !phone" ref="menuRef" class="tb-menu" :style="{ left: `${menuLeft}px`, top: menuTop === null ? undefined : `${menuTop}px` }" role="dialog" :aria-label="t('admin.accounts.table.moreFilters')" data-test="more-filters-menu">
      <div v-if="folded.length" class="tb-menu__folded">
        <span class="tb-menu__k">{{ t('admin.accounts.table.foldedTitle') }}</span>
        <div class="tb-menu__tags">
          <span v-for="tag in foldedTags" :key="tag.key" class="tb-tag">
            <span class="tb-tag__k">{{ label(tag.key) }}：</span>
            <span class="tb-tag__v">{{ tag.text }}</span>
            <button type="button" class="tb-tag__x" :aria-label="t('admin.accounts.table.removeFilter', { label: label(tag.key) })" @click="setFilter(tag.key, '')">
              <Icon name="x" size="xs" />
            </button>
          </span>
        </div>
      </div>
      <div v-for="key in menuKeys" :key="key" class="tb-menu__field">
        <span aria-hidden="true">{{ label(key) }}</span>
        <Select
          size="sm"
          class="tb-sel tb-sel--full"
          :model-value="filters[FIELD[key]] ?? ''"
          :options="selectOptions(key)"
          :aria-label="label(key)"
          :data-filter="key"
          @update:model-value="setFilter(key, String($event ?? ''))"
        />
      </div>
      <button v-if="tags.length" type="button" class="tb-menu__clear" @click="clearAll">{{ t('admin.accounts.table.clearFilters') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The accounts toolbar (spec 4.2, prototype) on the shared TableToolbar in its compact form (12.9), whose 32px search
 * box and buttons it uses. From 768px (a tablet too, mobile list plan) one row, measured; from 1024px it never wraps,
 * at 768–1023 the actions may take a row of their own (this
 * component's own rules below): search, four filters shown directly (platform, status, group, quality), "more filters"
 * (type, privacy, IQ, category) and the active filters as removable tags. Tags that do not fit fold from the right into
 * the "more filters" count, where they can still be seen and removed; if the row still overflows, the direct filters
 * fold into the menu too (quality first, then group, status, platform). All of it is measured on the rendered widths,
 * so any width from 768px up, with or without a classic scrollbar, keeps one row. On the right: the live indicator,
 * refresh, more actions (slot) and add.
 *
 * Below 768px it is TableToolbar's phone toolbar (filterSheet): the search, "Filters n" and "Add"; the chosen filters
 * as removable tags; the count (and the sort while its column is hidden), live state, refresh, "Columns" and "more
 * actions" on the info row. Every filter is in the bottom sheet, applied at once.
 */
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useEventListener, useMediaQuery, useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import LiveStatusIndicator from '@/components/common/LiveStatusIndicator.vue'
import Select from '@/components/common/Select.vue'
import TableToolbar from '@/components/layout/TableToolbar.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import TableSortHint from '@/components/common/TableSortHint.vue'
import { useTableViewport } from '@/composables/useTableViewport'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { LiveStatus } from '@/composables/useLiveTable'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import { useAuthStore } from '@/stores/auth'
import { useGroupCategoriesStore } from '@/stores/groupCategories'
import type { AdminGroup } from '@/types'
import { vTip } from '@/utils/tip'
import { PRIVACY_UNSET_FILTER, QUALITY_FILTERS, UNGROUPED_GROUP_FILTER } from './accountFilters'

export type ToolbarFilterKey = 'platform' | 'status' | 'group' | 'quality' | 'type' | 'privacy' | 'iq' | 'category'
export type ToolbarFilters = Partial<Record<'platform' | 'status' | 'group' | 'sched_quality' | 'type' | 'privacy_mode' | 'iq_status' | 'group_category', string>>

const props = defineProps<{
  filters: ToolbarFilters
  search: string
  groups: AdminGroup[]
  liveStatus: LiveStatus
  pause: () => void
  resume: () => void
  refreshing?: boolean
  canCreate?: boolean
  /** All results under the filters (the phone's info row). */
  total?: number
  /** The sort, while its column is hidden (no header shows it). */
  sortHint?: { label: string; order: 'asc' | 'desc' } | null
}>()
const emit = defineEmits<{
  'update:search': [value: string]
  /** One filter changed (the page scrolls up and reloads). */
  change: [filters: ToolbarFilters]
  refresh: []
  create: []
  /** The phone's "Columns" button. */
  columns: []
}>()

const { t } = useI18n()
const authStore = useAuthStore()
const categories = useGroupCategoriesStore()

const DIRECT: ToolbarFilterKey[] = ['platform', 'status', 'group', 'quality']
const MORE: ToolbarFilterKey[] = ['type', 'privacy', 'iq', 'category']
/** Direct filters fold into the menu in this order when even the tags folded away do not make room. */
const DIRECT_FOLD_ORDER: ToolbarFilterKey[] = ['quality', 'group', 'status', 'platform']
const FIELD: Record<ToolbarFilterKey, keyof ToolbarFilters> = {
  platform: 'platform',
  status: 'status',
  group: 'group',
  quality: 'sched_quality',
  type: 'type',
  privacy: 'privacy_mode',
  iq: 'iq_status',
  category: 'group_category'
}
const QUALITY_TONES: Record<string, StatusDotTone> = { normal: 'ok', rear: 'back', trial: 'info', unknown: 'observe', exempt: 'exempt' }

const label = (key: ToolbarFilterKey) => t(`admin.accounts.table.filterLabels.${key}`)

type Option = { value: string; label: string }
const optionsByKey = computed<Record<ToolbarFilterKey, Option[]>>(() => ({
  platform: CONCRETE_PLATFORM_OPTIONS.map((option) => ({ value: String(option.value), label: option.label })),
  status: [
    { value: 'active', label: t('admin.accounts.status.active') },
    { value: 'inactive', label: t('admin.accounts.status.inactive') },
    { value: 'error', label: t('admin.accounts.status.error') },
    { value: 'rate_limited', label: t('admin.accounts.status.rateLimited') },
    { value: 'temp_unschedulable', label: t('admin.accounts.status.tempUnschedulable') },
    { value: 'unschedulable', label: t('admin.accounts.status.unschedulable') }
  ],
  group: [
    { value: UNGROUPED_GROUP_FILTER, label: t('admin.accounts.ungroupedGroup') },
    ...props.groups.map((group) => ({ value: String(group.id), label: group.name }))
  ],
  quality: QUALITY_FILTERS.map((value) => ({ value, label: t(`admin.accounts.table.quality.${value}`) })),
  type: [
    { value: 'oauth', label: t('admin.accounts.oauthType') },
    { value: 'setup-token', label: t('admin.accounts.setupToken') },
    { value: 'apikey', label: t('admin.accounts.apiKey') },
    { value: 'bedrock', label: 'AWS Bedrock' }
  ],
  privacy: [
    { value: PRIVACY_UNSET_FILTER, label: t('admin.accounts.privacyUnset') },
    { value: 'training_off', label: 'Privacy' },
    { value: 'training_set_cf_blocked', label: 'CF' },
    { value: 'training_set_failed', label: 'Fail' }
  ],
  iq: [
    { value: 'smart', label: t('admin.accounts.iqCheckStatus.smart') },
    { value: 'degraded', label: t('admin.accounts.iqCheckStatus.degraded') },
    { value: 'unknown', label: t('admin.accounts.iqCheckStatus.unknown') }
  ],
  category: categories.categories.map((category) => ({ value: String(category.id), label: category.name }))
}))
const options = (key: ToolbarFilterKey) => optionsByKey.value[key]
/** A filter's Select: "all" (no value) first, then its options. */
const selectOptions = (key: ToolbarFilterKey): Option[] => [{ value: '', label: t('admin.accounts.table.any') }, ...options(key)]

const showCategory = computed(() => !authStore.isSimpleMode && (categories.categories.length > 0 || !!props.filters.group_category))
const moreKeys = computed(() => MORE.filter((key) => key !== 'category' || showCategory.value))

const tags = computed(() =>
  [...DIRECT, ...MORE].flatMap((key) => {
    const value = props.filters[FIELD[key]]
    if (!value) return []
    const text = options(key).find((option) => option.value === value)?.label ?? value
    return [{ key, value, text }]
  })
)

const setFilter = (key: ToolbarFilterKey, value: string) => {
  emit('change', { ...props.filters, [FIELD[key]]: value })
}
const clearAll = () => {
  const next: ToolbarFilters = { ...props.filters }
  for (const key of [...DIRECT, ...MORE]) next[FIELD[key]] = ''
  emit('change', next)
}

// ---- One row: fold tags, then direct filters ----
const rootRef = ref<HTMLElement | null>(null)
const filtersRef = ref<HTMLElement | null>(null)
const tagsRef = ref<HTMLElement | null>(null)
const moreRef = ref<HTMLElement | null>(null)
const menuRef = ref<HTMLElement | null>(null)
const searchRef = ref<HTMLInputElement | null>(null)
const folded = ref<ToolbarFilterKey[]>([])
const foldedSelects = ref<ToolbarFilterKey[]>([])
const foldedTags = computed(() => tags.value.filter((tag) => folded.value.includes(tag.key)))
const menuKeys = computed(() => [...foldedSelects.value, ...moreKeys.value])

const TAG_GAP = 6
/** Tags from the left that fit the tag box; the rest fold (a tag folded already stays folded). */
function foldTags(): ToolbarFilterKey[] {
  const box = tagsRef.value
  if (!box) return []
  const room = box.clientWidth
  const elements = Array.from(box.querySelectorAll<HTMLElement>('.tb-tag'))
  let used = 0
  const out: ToolbarFilterKey[] = []
  for (const element of elements) {
    const key = element.dataset.key as ToolbarFilterKey
    if (element.hidden) {
      out.push(key)
      continue
    }
    used += element.offsetWidth + (used ? TAG_GAP : 0)
    if (used > room + 1) out.push(key)
  }
  return out
}
/**
 * Folds tags until the ones shown fit. Each fold is measured again: the count it puts on "more filters" widens that
 * button and narrows the tag box, which can push one more tag out (7e-3).
 */
async function foldTagsUntilStable(): Promise<ToolbarFilterKey[]> {
  let next = foldTags()
  // Measuring only means anything from 768px; the width can cross it between two rounds (7l-7).
  while (wide.value) {
    folded.value = next
    await nextTick()
    const again = foldTags()
    if (again.length === next.length) return next
    next = again
  }
  return next
}
/** The direct filters run past the filter row's box (TableToolbar's filters slot, the room left after the search). */
const filtersOverflow = () => {
  const filters = filtersRef.value?.getBoundingClientRect()
  const row = filtersRef.value?.parentElement?.getBoundingClientRect()
  return !!filters && !!row && filters.right + 8 > row.right + 0.5
}
/**
 * The one-row layout and its measuring hold from 768px (mobile list plan: a tablet takes the desktop toolbar). Below
 * 768px the filters are in the phone's bottom sheet: nothing is measured or folded, so "more filters" counts nothing.
 */
const wide = useMediaQuery('(min-width: 768px)')
const { phone, desktop } = useTableViewport()
/** The phone's sheet lists every filter: the four direct ones, then the rest. */
const sheetKeys = computed(() => [...DIRECT, ...moreKeys.value])
watch(wide, () => {
  void fit()
})

let fitting = false
/** A fit was asked for while one ran (a resize, a tag's text arriving): it runs again on what is there now. */
let refit = false
async function fit() {
  if (!rootRef.value) return
  if (fitting) {
    refit = true
    return
  }
  fitting = true
  try {
    do {
      refit = false
      folded.value = []
      foldedSelects.value = []
      if (!wide.value) break
      await nextTick()
      const next = await foldTagsUntilStable()
      if (wide.value && next.length === tags.value.length) {
        const selects: ToolbarFilterKey[] = []
        for (const key of DIRECT_FOLD_ORDER) {
          if (!wide.value || !filtersOverflow()) break
          selects.push(key)
          foldedSelects.value = [...selects]
          await nextTick()
        }
        if (selects.length) {
          folded.value = []
          await nextTick()
          await foldTagsUntilStable()
        }
      }
    } while (refit)
  } finally {
    fitting = false
  }
  // The width crossed 768px while measuring: nothing stays folded (7l-7).
  if (!wide.value) {
    folded.value = []
    foldedSelects.value = []
  }
  if (menuOpen.value) placeMenu()
}

// The filters' row (TableToolbar's filter box, the room left beside the search and the actions) also changes when
// only the search or the actions change width, which the toolbar's own size does not show; and once a width change
// is over, everything is measured once more, on the settled layout (7q-7).
const filterBox = () => filtersRef.value?.parentElement ?? null
let settleFit: ReturnType<typeof setTimeout> | undefined
useResizeObserver([rootRef, filterBox], () => {
  void fit()
  clearTimeout(settleFit)
  settleFit = setTimeout(() => void fit(), 200)
})
onUnmounted(() => clearTimeout(settleFit))
watch(() => tags.value.map((tag) => `${tag.key}:${tag.text}`).join('|'), () => {
  void fit()
})

// ---- The menu: under its button (kept inside the toolbar); closes on Escape and on a click outside it and its button ----
const menuOpen = ref(false)
const menuLeft = ref(0)
/** Null until placed: the stylesheet's spot under the toolbar meanwhile. */
const menuTop = ref<number | null>(null)
/** Under its button, on the one row (from 768px). */
function placeMenu() {
  const root = rootRef.value?.getBoundingClientRect()
  const button = moreRef.value?.getBoundingClientRect()
  if (!root || !button) return
  const width = menuRef.value?.offsetWidth ?? 0
  menuLeft.value = Math.max(0, Math.min(button.left - root.left, root.width - width))
  menuTop.value = button.bottom - root.top + 6
}
// The "more filters" menu belongs to the row layout.
watch(phone, (on) => { if (on) menuOpen.value = false })
watch(menuOpen, async (open) => {
  if (!open) return
  await nextTick()
  placeMenu()
})
// A sub-layer of the overlay stack: a Select opened in the menu is a layer above it, its panel in the body. A press
// in that panel is not a press outside the menu, so the option's click lands and the menu stays open; Escape closes
// the open Select first, then the menu (12.12-3).
const menuLayer = useOverlayLayer(menuOpen, {
  root: menuRef,
  modal: false,
  onEscape: () => {
    menuOpen.value = false
    moreRef.value?.focus({ preventScroll: true })
  },
  onCovered: () => { menuOpen.value = false }
})
useEventListener(document, 'mousedown', (event: MouseEvent) => {
  if (!menuOpen.value) return
  const target = event.target as Element
  if (menuRef.value?.contains(target) || moreRef.value?.contains(target) || menuLayer.insideAbove(target)) return
  menuOpen.value = false
})

onMounted(() => {
  if (!authStore.isSimpleMode) void categories.reload()
  void fit()
})

defineExpose({ focusSearch: () => searchRef.value?.focus() })
</script>

<style scoped>
@reference "../../../../style.css";

/* The menu is placed against the whole toolbar. */
.tb-root {
  position: relative;
  min-width: 0;
}
/* From 768px the page's own one-row toolbar, on TableToolbar's compact parts (8px apart, the 220px search box): the
   tags taking the room left. From 1024px the actions keep to that row; at 768–1023 they may take a row of their own
   (TableToolbar's rule), the search, filters and tags still on one measured row. */
@media (min-width: 768px) {
  /* Its basis stays the search box's, not the shared one (its content, 7r-5): one row whatever the tags, the actions
     at their size and the tags folding into the room they leave. */
  .tb :deep(.table-toolbar__main) {
    flex: 1 1 16rem;
    flex-wrap: nowrap;
  }
  .tb :deep(.table-toolbar__filters) {
    flex: 1 1 auto;
    flex-wrap: nowrap;
  }
  .tb :deep(.table-toolbar__filters > .tb-tags) {
    flex: 1 1 0;
  }
  .tb :deep(.table-toolbar__actions) {
    flex-wrap: nowrap;
  }
  /* Its own fixed search box, 220px (176px at 768–1279, below), not the shared flexible one (12.12-10): this row
     folds its filters into "more filters" instead. */
  .tb.table-toolbar--compact :deep(.table-toolbar__search) {
    flex: none;
    width: 220px;
  }
  /* The direct filters' triggers spaced tighter than the shared sm (10px sides, 6px gap), so a filter's name fits whole
     at 96 and 88px (12.12-14): 8px before the text, 2px to the chevron, 4px after it (the chevron is drawn 3px inside
     its 16px box). Text room: width − 2 border − 8 − 2 − 16 − 4, so 64px and 56px, against about 52px for the longest
     names ("质量状态", "Platform"). */
  .tb-filters .tb-sel :deep(.select-trigger--sm) {
    gap: 2px;
    padding: 0 4px 0 8px;
  }
}
@media (min-width: 1024px) {
  .tb {
    flex-wrap: nowrap;
    align-items: center;
  }
}
.tb-filters {
  display: flex;
  flex: none;
  align-items: center;
  gap: 8px;
}
.tb-tags {
  display: flex;
  flex-wrap: nowrap;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
}
.tb-sel {
  width: 96px;
  flex: none;
}
.tb-sel--full {
  width: 100%;
}
/* The phone's sheet: one field per line, its name above the full-width select. */
.tb-sheet__field {
  display: grid;
  gap: 6px;
}
.tb-sheet__k {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
  line-height: var(--line-height-small);
}
.tb-sort {
  margin-right: 8px;
}
.tb-tag__x:focus-visible,
.tb-menu__clear:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.tb-count {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--color-bg-pill-hover);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  font-variant-numeric: tabular-nums;
  line-height: 18px;
  text-align: center;
}
.tb-tag {
  display: inline-flex;
  height: 24px;
  flex: none;
  align-items: center;
  gap: 2px;
  padding: 0 2px 0 8px;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  font-size: var(--font-size-small);
  white-space: nowrap;
}
.tb-tag[hidden] {
  display: none;
}
.tb-tag__k {
  color: var(--color-text-secondary);
}
.tb-tag__v {
  color: var(--color-text-primary);
}
.tb-tag__dot {
  margin: 0 4px 0 2px;
}
.tb-tag__x {
  display: inline-grid;
  width: 20px;
  height: 20px;
  place-items: center;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
}
.tb-tags .tb-tag__x:focus-visible {
  outline-offset: -2px;
}
@media (hover: hover) {
  .tb-tag__x:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.tb-menu {
  position: absolute;
  z-index: 35;
  top: calc(100% + 6px);
  left: 0;
  display: grid;
  min-width: 260px;
  gap: 12px;
  padding: 12px;
  border: 1px solid var(--color-line-popover);
  border-radius: var(--radius-popover);
  background: var(--color-bg-popover);
  box-shadow: var(--shadow-dropdown);
}
.tb-menu__folded {
  display: grid;
  gap: 6px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--color-line-row);
}
.tb-menu__k,
.tb-menu__field > span:first-child {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.tb-menu__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.tb-menu__field {
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr);
  align-items: center;
  gap: 8px;
}
.tb-menu__clear {
  justify-self: start;
  height: 24px;
  padding: 0 6px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
@media (hover: hover) {
  .tb-menu__clear:hover {
    background: var(--color-bg-popover-hover);
    color: var(--color-text-primary);
  }
}
/* Touch (7n-3): a tag's remove button reaches 40 x 40 with an invisible area, more of it over its own tag than towards
   the next one (6px, the gap). From 768px the tag row clips, so it lends the area 8px above and below. */
@media (pointer: coarse) {
  .tb-tag__x {
    position: relative;
  }
  .tb-tag__x::after {
    position: absolute;
    inset: -10px -6px -10px -14px;
    content: '';
  }
}
@media (min-width: 768px) and (pointer: coarse) {
  .tb-tags {
    margin-block: -8px;
    padding-block: 8px;
  }
}
/* 768–1279: the search box and filters narrow (two classes: over the shared compact 220px). 176px holds the whole hint
   "名称、备注或 ID" (160px cut it). */
@media (min-width: 768px) and (max-width: 1279px) {
  .tb.table-toolbar--compact :deep(.table-toolbar__search) {
    width: 176px;
  }
  /* The direct filters on the toolbar only: the fields in "more filters" keep filling their column (12.12-4). */
  .tb-filters .tb-sel {
    width: 88px;
  }
}
</style>
