<template>
  <div ref="rootRef" class="tb-root" data-test="accounts-toolbar">
    <TableToolbar class="tb">
      <template #search>
      <label class="tb-search">
        <span class="sr-only">{{ t('admin.accounts.table.searchPlaceholder') }}</span>
        <Icon name="search" size="sm" class="tb-search__icon" />
        <input
          ref="searchRef"
          class="tb-input"
          type="search"
          autocomplete="off"
          :value="search"
          :placeholder="t('admin.accounts.table.searchPlaceholder')"
          data-test="accounts-search"
          @input="emit('update:search', ($event.target as HTMLInputElement).value)"
        />
      </label>
      </template>
      <template #filters>
      <div ref="filtersRef" class="tb-filters">
        <template v-for="key in DIRECT" :key="key">
          <span v-if="!foldedSelects.includes(key)" class="tb-sel">
            <select
              :class="{ 'has-value': !!filters[FIELD[key]] }"
              :value="filters[FIELD[key]] ?? ''"
              :aria-label="label(key)"
              :data-filter="key"
              @change="setFilter(key, ($event.target as HTMLSelectElement).value)"
            >
              <option value="">{{ label(key) }}</option>
              <option v-for="option in options(key)" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <Icon name="chevronDown" size="xs" class="tb-sel__icon" />
          </span>
        </template>
        <button
          ref="moreRef"
          type="button"
          class="tb-btn tb-btn--secondary"
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

      <template #status>
      <LiveStatusIndicator :status="liveStatus" :pause="pause" :resume="resume" :detail="t('admin.accounts.table.liveDetail')" />
      </template>
      <template #actions>
      <button
        type="button"
        class="tb-icon-btn table-toolbar__icon"
        :aria-label="t('admin.accounts.table.refresh')"
        v-tip="t('admin.accounts.table.refreshTip')"
        data-test="accounts-refresh"
        @click="emit('refresh')"
      >
        <Icon name="refresh" size="sm" :class="{ 'animate-spin': refreshing }" />
      </button>
      <slot name="more-actions" />
      <button v-if="canCreate" type="button" class="tb-btn tb-btn--primary" data-test="accounts-create" @click="emit('create')">
        <Icon name="plus" size="sm" />{{ t('admin.accounts.createAccount') }}
      </button>
      </template>
    </TableToolbar>

    <div v-if="menuOpen" ref="menuRef" class="tb-menu" :style="{ left: `${menuLeft}px`, top: menuTop === null ? undefined : `${menuTop}px` }" role="dialog" :aria-label="t('admin.accounts.table.moreFilters')" data-test="more-filters-menu">
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
      <label v-for="key in menuKeys" :key="key" class="tb-menu__field">
        <span>{{ label(key) }}</span>
        <span class="tb-sel tb-sel--full">
          <select :value="filters[FIELD[key]] ?? ''" :class="{ 'has-value': !!filters[FIELD[key]] }" :data-filter="key" @change="setFilter(key, ($event.target as HTMLSelectElement).value)">
            <option value="">{{ t('admin.accounts.table.any') }}</option>
            <option v-for="option in options(key)" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
          <Icon name="chevronDown" size="xs" class="tb-sel__icon" />
        </span>
      </label>
      <button v-if="tags.length" type="button" class="tb-menu__clear" @click="clearAll">{{ t('admin.accounts.table.clearFilters') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The accounts toolbar (spec 4.2, prototype) on the shared TableToolbar. From 1024px one row, never wrapping (this
 * component's own rules below): search, four filters shown directly (platform, status, group, quality), "more filters"
 * (type, privacy, IQ, category) and the active filters as removable tags. Tags that do not fit fold from the right into
 * the "more filters" count, where they can still be seen and removed; if the row still overflows, the direct filters
 * fold into the menu too (quality first, then group, status, platform). All of it is measured on the rendered widths,
 * so any width from 1024px up, with or without a classic scrollbar, keeps one row. On the right: the live indicator,
 * refresh, more actions (slot) and add.
 *
 * Below 1024px it takes TableToolbar's narrow layout: search on its own row, the filters on one row that scrolls
 * sideways, the active filters as tags wrapping on a row under it (7l-3), the live indicator and the actions last.
 */
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useEventListener, useMediaQuery, useResizeObserver } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import LiveStatusIndicator from '@/components/common/LiveStatusIndicator.vue'
import TableToolbar from '@/components/layout/TableToolbar.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { LiveStatus } from '@/composables/useLiveTable'
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
}>()
const emit = defineEmits<{
  'update:search': [value: string]
  /** One filter changed (the page scrolls up and reloads). */
  change: [filters: ToolbarFilters]
  refresh: []
  create: []
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
const QUALITY_TONES: Record<string, StatusDotTone> = { normal: 'ok', substandard: 'back', exempt: 'exempt', observing: 'observe' }

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
  // Measuring only means anything from 1024px; the width can cross it between two rounds (7l-7).
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
 * Below 1024px the filter row scrolls sideways and the tags wrap on a row of their own under it (prototype at 390,
 * 7l-3): nothing is measured or folded, so "more filters" counts nothing.
 */
const wide = useMediaQuery('(min-width: 1024px)')
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
  // The width crossed 1024px while measuring: nothing stays folded (7l-7).
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
/** Under its button: on one row from 1024px, in the scrolling filter row below it. */
function placeMenu() {
  const root = rootRef.value?.getBoundingClientRect()
  const button = moreRef.value?.getBoundingClientRect()
  if (!root || !button) return
  const width = menuRef.value?.offsetWidth ?? 0
  menuLeft.value = Math.max(0, Math.min(button.left - root.left, root.width - width))
  menuTop.value = button.bottom - root.top + 6
}
watch(menuOpen, async (open) => {
  if (!open) return
  await nextTick()
  placeMenu()
})
useEventListener(document, 'mousedown', (event: MouseEvent) => {
  if (!menuOpen.value) return
  const target = event.target as Node
  if (menuRef.value?.contains(target) || moreRef.value?.contains(target)) return
  menuOpen.value = false
})
useEventListener(document, 'keydown', (event: KeyboardEvent) => {
  if (event.key !== 'Escape' || !menuOpen.value) return
  event.preventDefault()
  menuOpen.value = false
  moreRef.value?.focus({ preventScroll: true })
})

onMounted(() => {
  if (!authStore.isSimpleMode) void categories.reload()
  void fit()
})

defineExpose({ focusSearch: () => searchRef.value?.focus() })
</script>

<style scoped>
/* The menu is placed against the whole toolbar. */
.tb-root {
  position: relative;
  min-width: 0;
}
/* From 1024px the page's own one-row toolbar, on TableToolbar's parts: no wrapping, 8px apart, the tags taking the
   room left; below 1024px TableToolbar's narrow layout applies as it is. */
@media (min-width: 1024px) {
  .tb {
    flex-wrap: nowrap;
    align-items: center;
    gap: 8px;
  }
  /* Its basis stays the search box's, not the shared one (its content, 7r-5): one row whatever the tags, the actions
     at their size and the tags folding into the room they leave. */
  .tb :deep(.table-toolbar__main) {
    flex: 1 1 16rem;
    flex-wrap: nowrap;
    gap: 8px;
  }
  .tb :deep(.table-toolbar__search) {
    width: auto;
  }
  .tb :deep(.table-toolbar__filters) {
    flex: 1 1 auto;
    flex-wrap: nowrap;
    gap: 8px;
  }
  .tb :deep(.table-toolbar__filters > .tb-tags) {
    flex: 1 1 0;
  }
  .tb :deep(.table-toolbar__actions) {
    flex-wrap: nowrap;
    gap: 8px;
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
/* Below 1024px (prototype at 390, 7l-3): the shared filter box steps aside, so the selects scroll sideways on their own
   row, reaching out to the screen's edges as TableToolbar's row does, and the tags wrap on a row under it. */
@media (max-width: 1023px) {
  .tb :deep(.table-toolbar__filters) {
    display: contents;
  }
  .tb-filters {
    min-width: 0;
    margin-inline: -16px;
    padding-inline: 16px;
    overflow-x: auto;
    scroll-padding-inline: 16px;
    scrollbar-width: none;
  }
  .tb-filters::-webkit-scrollbar {
    display: none;
  }
  .tb-tags {
    flex-wrap: wrap;
    overflow: visible;
  }
  .tb-tags:empty {
    display: none;
  }
}
@media (min-width: 768px) and (max-width: 1023px) {
  .tb-filters {
    margin-inline: -24px;
    padding-inline: 24px;
    scroll-padding-inline: 24px;
  }
}
@media (max-width: 1023px) and (pointer: fine) {
  .tb-filters {
    scrollbar-color: var(--color-line-control) transparent;
    scrollbar-width: thin;
  }
  .tb-filters::-webkit-scrollbar {
    display: block;
    height: 6px;
  }
  .tb-filters::-webkit-scrollbar-thumb {
    border-radius: 3px;
    background: var(--color-line-control);
  }
}
.tb-search {
  position: relative;
  display: block;
  flex: none;
  width: 220px;
}
.tb-search__icon {
  position: absolute;
  top: 50%;
  left: 10px;
  width: 14px;
  height: 14px;
  margin-top: -7px;
  color: var(--color-text-muted);
  pointer-events: none;
}
.tb-input {
  width: 100%;
  height: var(--control-height);
  padding: 0 10px 0 30px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
}
.tb-input::placeholder {
  color: var(--color-text-muted);
}
.tb-input:focus-visible,
.tb-sel select:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -1px;
  border-color: transparent;
}
.tb-sel {
  position: relative;
  display: inline-block;
  flex: none;
}
.tb-sel select {
  width: 96px;
  height: var(--control-height);
  padding: 0 28px 0 10px;
  overflow: hidden;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  appearance: none;
  background: var(--color-bg-control);
  color: var(--color-text-secondary);
  font-size: var(--font-size-body);
  text-overflow: ellipsis;
}
.tb-sel select.has-value {
  color: var(--color-text-primary);
}
.tb-sel--full,
.tb-sel--full select {
  width: 100%;
}
.tb-sel__icon {
  position: absolute;
  top: 50%;
  right: 9px;
  width: 12px;
  height: 12px;
  margin-top: -6px;
  color: var(--color-text-secondary);
  pointer-events: none;
}
@media (hover: hover) {
  .tb-sel select:hover {
    background: var(--color-bg-control-hover);
  }
}
.tb-btn {
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
  transition: background-color var(--motion-fast) var(--ease-out);
}
.tb-btn--secondary {
  border-color: var(--color-line-control);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
}
.tb-btn--primary {
  background: theme('colors.primary.500');
  color: #fff;
}
@media (hover: hover) {
  .tb-btn--secondary:hover {
    background: var(--color-bg-control-hover);
  }
  .tb-btn--primary:hover {
    background: theme('colors.primary.600');
  }
}
.tb-btn:focus-visible,
.tb-icon-btn:focus-visible,
.tb-tag__x:focus-visible,
.tb-menu__clear:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.tb-icon-btn {
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
  .tb-icon-btn:hover {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.tb-count {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--color-bg-pill-hover);
  color: var(--color-text-primary);
  font-size: var(--font-size-micro);
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
  box-shadow: var(--shadow-popover);
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
/* Below 1024px (prototype, touch): the search fills its row, controls are 40px high, the tags live in the menu. */
@media (max-width: 1023px) {
  .tb-search {
    width: 100%;
  }
  .tb-input {
    height: 40px;
    font-size: 15px;
  }
  .tb-sel select,
  .tb-btn {
    height: 40px;
  }
}
/* Touch (7n-3): a tag's remove button reaches 40 x 40 with an invisible area, more of it over its own tag than towards
   the next one (6px, the gap). From 1024px the tag row clips, so it lends the area 8px above and below. */
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
@media (min-width: 1024px) and (pointer: coarse) {
  .tb-tags {
    margin-block: -8px;
    padding-block: 8px;
  }
}
/* 1024–1279: still one row; the search box and filters narrow */
@media (min-width: 1024px) and (max-width: 1279px) {
  .tb-search {
    width: 160px;
  }
  .tb-sel select {
    width: 88px;
  }
}
</style>
