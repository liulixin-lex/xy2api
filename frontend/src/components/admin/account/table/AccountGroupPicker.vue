<template>
  <Teleport to="body">
    <div
      ref="rootRef"
      class="gp"
      role="dialog"
      :aria-label="t('admin.groupCategories.editAccountGroups', { name: account.name })"
      :aria-busy="phase === 'saving'"
      :style="{ top: `${position.top}px`, left: `${position.left}px`, zIndex: layer.zIndex.value }"
      data-group-picker
    >
      <div class="gp-top">
        <label class="gp-search">
          <span class="sr-only">{{ t('admin.accounts.groupPicker.searchLabel') }}</span>
          <Icon name="search" size="sm" class="gp-search__icon" />
          <input
            ref="searchRef"
            v-model="query"
            class="gp-search__input"
            type="search"
            autocomplete="off"
            :placeholder="t('admin.accounts.groupPicker.search')"
            data-test="group-picker-search"
          />
        </label>
        <div v-if="tabs.length" class="gp-tabs" role="tablist" :aria-label="t('admin.accounts.groupPicker.categories')">
          <button type="button" role="tab" class="gp-tab" :aria-selected="activeId === null" @click="activeId = null">{{ t('admin.accounts.groupPicker.all') }}</button>
          <button
            v-for="category in tabs"
            :key="category.id"
            type="button"
            role="tab"
            class="gp-tab"
            :aria-selected="activeId === category.id"
            :data-test="`group-picker-tab-${category.id}`"
            @click="activeId = category.id"
          >{{ category.name }}</button>
        </div>
      </div>

      <div class="gp-list" data-test="group-picker-list">
        <div v-if="!fresh" class="gp-empty">{{ t('common.loading') }}</div>
        <div v-else-if="!visible.length" class="gp-empty">{{ query.trim() ? t('admin.accounts.groupPicker.noMatch', { q: query.trim() }) : t('admin.accounts.groupPicker.none') }}</div>
        <label v-for="group in visible" :key="group.id" class="gp-item" :class="{ 'is-checked': selectedSet.has(group.id) }">
          <input
            type="checkbox"
            class="gp-check"
            :value="group.id"
            :checked="selectedSet.has(group.id)"
            :disabled="phase !== 'edit'"
            @change="toggle(group.id, ($event.target as HTMLInputElement).checked)"
          />
          <GroupAliasPill :group="group" />
          <span class="gp-name" :title="group.name">{{ group.name }}</span>
          <span v-if="tagOf(group)" class="gp-tag">{{ tagOf(group) }}</span>
        </label>
      </div>

      <div v-if="phase !== 'conflict'" class="gp-foot" data-test="group-picker-foot">
        {{ t('admin.accounts.groupPicker.selected', { n: selected.length }) }} · {{ phase === 'saving' ? t('admin.accounts.groupPicker.saving') : t('admin.accounts.groupPicker.closeToSave') }}
      </div>
      <div v-else class="gp-conflict" data-test="group-picker-conflict">
        <div class="gp-conflict__msg">
          <Icon name="exclamationTriangle" size="sm" class="gp-conflict__icon" />
          <div>
            {{ t('admin.accounts.mixedChannelWarningTitle') }}
            <span class="gp-conflict__sub" data-test="group-picker-conflict-detail">{{ conflictDetails ? t('admin.accounts.mixedChannelWarning', conflictDetails) : t('admin.accounts.groupPicker.conflict') }}</span>
          </div>
        </div>
        <div class="gp-conflict__acts">
          <button type="button" class="btn btn-secondary btn-sm" data-test="group-picker-cancel" @click="cancelConflict">{{ t('admin.accounts.groupPicker.cancel') }}</button>
          <button ref="confirmRef" type="button" class="btn btn-primary btn-sm" data-test="group-picker-confirm" @click="confirmConflict">{{ t('admin.accounts.groupPicker.confirm') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * The groups column's group picker (spec 4.3, prototype): anchored beside the cell, about 280px wide and kept inside the
 * viewport. A search over alias and name, the category tabs, and the groups the account's platform can take (the
 * rule of the group selector), each with its alias pill and full name.
 *
 * Ticking does not save. Closing (a click outside, Escape) saves once when the selection changed, through the page's
 * `save` (an optimistic edit of this cell that rolls back on failure); the picker shows "saving" until it is answered.
 * A mixed-channel conflict (HTTP 409) is confirmed inside the picker with the focus on the confirm button: confirming
 * sends it again with the risk flag, cancelling keeps the original groups (the failed edit was rolled back).
 * Background updates of the row do not reach the selection while it is open.
 */
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useEventListener, useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores'
import { useGroupCategoryTabs } from '@/composables/useGroupCategoryTabs'
import { useOverlayLayer } from '@/composables/useOverlayLayer'
import { eligibleGroupsFor, mergeSelectableGroups, sameIdList } from '@/utils/accountGroupSelection'
import { extractApiErrorMessage } from '@/utils/apiError'
import { platformLabel } from '@/utils/platformColors'
import type { Account, AdminGroup, Group } from '@/types'
import { orderGroups } from './accountGroups'

const props = defineProps<{
  /** The account as it was when the picker opened. */
  account: Pick<Account, 'id' | 'name' | 'platform'>
  groups: AdminGroup[]
  /** The cell it is anchored to. */
  anchor: HTMLElement | null
  /** Opened from the keyboard: the search field takes focus. */
  focusSearch?: boolean
  /** The pill (or button) it was opened from: focus goes back to it when the picker closes (7i-4). */
  opener?: HTMLElement | null
  /** Saves the groups (the page's optimistic edit of the cell); rejects with the request's error. */
  save: (groupIds: number[], confirmMixedChannel: boolean) => Promise<unknown>
}>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()

const rootRef = ref<HTMLElement | null>(null)
const searchRef = ref<HTMLInputElement | null>(null)
const confirmRef = ref<HTMLButtonElement | null>(null)
const query = ref('')
const fresh = ref<Account | null>(null)
const selected = ref<number[]>([])
let initial: number[] = []
const phase = ref<'edit' | 'saving' | 'conflict'>('edit')
/** The groups in conflict as the account editor names them; till the check answers, the short question (7n-4). */
const conflictDetails = ref<{ groupName: string; currentPlatform: string; otherPlatform: string } | null>(null)
let closed = false

const layer = useOverlayLayer(() => true, {
  root: rootRef,
  modal: false,
  floor: 60,
  onEscape: () => (phase.value === 'conflict' ? cancelConflict() : void requestClose()),
  onCovered: () => void requestClose()
})

// ---- Groups: eligible for the account's platform, plus inactive groups it is still in (they show ticked) ----
const eligible = computed(() => {
  const account = fresh.value
  if (!account) return []
  const merged = mergeSelectableGroups(props.groups, account) as Array<AdminGroup | Group>
  const list = eligibleGroupsFor(merged, account.platform, { mixedScheduling: account.extra?.mixed_scheduling === true, simpleMode: authStore.isSimpleMode })
  return orderGroups(list)
})
const { tabs, activeId, activeGroupIds } = useGroupCategoryTabs(() => true, () => eligible.value)
const visible = computed(() => {
  let list = eligible.value
  if (activeGroupIds.value) list = list.filter((group) => activeGroupIds.value!.has(group.id))
  const q = query.value.trim().toLowerCase()
  if (q) {
    list = list.filter((group) =>
      [group.name, (group as AdminGroup).alias, group.description].some((text) => !!text && text.toLowerCase().includes(q))
    )
  }
  return list
})
const selectedSet = computed(() => new Set(selected.value))

/** A group of another platform (mixed scheduling, composite) or one no longer active says so. */
function tagOf(group: AdminGroup | Group) {
  if (group.status && group.status !== 'active') return t('admin.accounts.groupPicker.inactive')
  return group.platform !== props.account.platform ? platformLabel(group.platform) : ''
}

/** Newly ticked groups go last; the account's groups keep their order. */
function toggle(id: number, checked: boolean) {
  if (phase.value !== 'edit') return
  selected.value = checked ? [...selected.value.filter((item) => item !== id), id] : selected.value.filter((item) => item !== id)
}

// The list row carries neither inactive groups nor the groups' order: the account is read once when the picker opens.
onMounted(async () => {
  place()
  if (props.focusSearch) searchRef.value?.focus({ preventScroll: true })
  try {
    const account = await adminAPI.accounts.getById(props.account.id)
    if (closed) return
    initial = [...(account.group_ids ?? [])]
    selected.value = [...initial]
    fresh.value = account
    await nextTick()
    place()
  } catch (error) {
    if (closed) return
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
    finish()
  }
})

// ---- Closing ----
/** The conflict's confirm had focus when it was pressed; the save that follows takes the button away (7i-4). */
let confirmedFromInside = false
function finish() {
  if (closed) return
  closed = true
  const active = document.activeElement
  const hadFocus = !!rootRef.value?.contains(active) || (confirmedFromInside && (!active || active === document.body))
  const id = props.account.id
  emit('close')
  if (hadFocus) {
    void nextTick(() => {
      const usable = (el: HTMLElement | null | undefined): el is HTMLElement => !!el?.isConnected && !el.matches(':disabled')
      // The pill it was opened from, else (that pill went with the edit) the account's first one.
      const opener = usable(props.opener) ? props.opener : Array.from(document.querySelectorAll<HTMLElement>(`[data-groups-opener="${id}"]`)).find(usable)
      opener?.focus({ preventScroll: true })
    })
  }
}

const isMixedChannelConflict = (error: unknown) => {
  const apiError = error as { status?: number; error?: string } | null
  return apiError?.status === 409 && apiError.error === 'mixed_channel_warning'
}

/**
 * The save's 409 carries only the backend's English sentence; the mixed-channel check names the group and the two
 * platforms, which the account editor's wording takes (7n-4). Without an answer the short question stands.
 */
async function explainConflict() {
  const groupIds = [...selected.value]
  try {
    const { details } = await adminAPI.accounts.checkMixedChannelRisk({ platform: props.account.platform, group_ids: groupIds, account_id: props.account.id })
    if (phase.value !== 'conflict' || !details) return
    conflictDetails.value = { groupName: details.group_name || 'Unknown', currentPlatform: details.current_platform || 'Unknown', otherPlatform: details.other_platform || 'Unknown' }
  } catch {
    // The short question is enough to decide on.
  }
}

/** The save under way: a close asked for meanwhile waits for it. */
let saving: Promise<void> | null = null
function submit(confirmMixedChannel: boolean): Promise<void> {
  saving = runSave(confirmMixedChannel).finally(() => { saving = null })
  return saving
}
async function runSave(confirmMixedChannel: boolean) {
  phase.value = 'saving'
  try {
    await props.save([...selected.value], confirmMixedChannel)
    phase.value = 'edit'
    finish()
  } catch (error) {
    if (!confirmMixedChannel && isMixedChannelConflict(error)) {
      conflictDetails.value = null
      phase.value = 'conflict'
      focusConfirm()
      void explainConflict()
      return
    }
    phase.value = 'edit'
    appStore.showError(extractApiErrorMessage(error, t('admin.accounts.failedToUpdate')))
    finish()
  }
}

/** A click outside also moves the focus where it lands; the confirm button takes it back once that is done. */
function focusConfirm() {
  const focus = () => { if (phase.value === 'conflict') confirmRef.value?.focus({ preventScroll: true }) }
  void nextTick(focus)
  setTimeout(focus, 0)
}

/**
 * Closes the picker: at once without a change, else after the save. Resolves true once it is closed, false while it
 * stays open (a conflict to confirm). Asked during a save, it answers when that save is over (7f-5): the page then
 * opens the picker another pill asked for, unless the save stopped on a conflict.
 */
async function requestClose(): Promise<boolean> {
  if (closed) return true
  if (phase.value === 'conflict') {
    focusConfirm()
    return false
  }
  if (saving) {
    await saving
    return closed
  }
  if (!fresh.value || sameIdList(selected.value, initial)) {
    finish()
    return true
  }
  await submit(false)
  return closed
}

function confirmConflict() {
  if (phase.value !== 'conflict') return
  confirmedFromInside = !!rootRef.value?.contains(document.activeElement)
  void submit(true)
}

/** The failed edit was rolled back: the cell shows the original groups again. */
function cancelConflict() {
  if (phase.value === 'conflict') finish()
}

// A pointerdown outside closes it; the click goes on. A pill of the groups column is the page's to handle (the same
// account's pill closes the picker, another one closes it and opens that account's).
useEventListener(document, 'pointerdown', (event: PointerEvent) => {
  const target = event.target as Element | null
  if (!target?.isConnected || rootRef.value?.contains(target) || layer.insideAbove(target) || target.closest('[data-groups-opener]')) return
  void requestClose()
}, { capture: true })

// ---- Position: beside the cell from 1024px; below 1024px under the card's "edit groups" row, right-aligned
// (prototype "分组浮层" at 390), above it when there is no room below. Always inside the viewport. ----
const position = reactive({ top: 0, left: 0 })
const GAP = 4
const MARGIN = 8
/** The page header's bottom: the picker never covers it (7l-6). */
const HEADER = 64
const wide = useMediaQuery('(min-width: 1024px)')
// Across 1024px (a tablet turned) the cell or card it is anchored to goes: it closes, saving a change (7l-6).
watch(wide, () => void requestClose())
function place() {
  const panel = rootRef.value
  const anchor = props.anchor
  if (!panel || !anchor?.isConnected) return
  const rect = anchor.getBoundingClientRect()
  const width = panel.offsetWidth
  const height = panel.offsetHeight
  const viewportWidth = document.documentElement.clientWidth || window.innerWidth
  const clampLeft = (left: number) => Math.round(Math.max(MARGIN, Math.min(left, viewportWidth - width - MARGIN)))
  const clampTop = (top: number) => Math.round(Math.max(HEADER, Math.min(top, window.innerHeight - height - MARGIN)))
  if (!wide.value) {
    position.left = clampLeft(rect.right - width)
    const below = rect.bottom + GAP
    position.top = clampTop(below + height > window.innerHeight - MARGIN ? rect.top - height - GAP : below)
    return
  }
  let left = rect.right + GAP
  if (left + width > viewportWidth - MARGIN) left = rect.left - width - GAP
  position.left = clampLeft(left)
  position.top = clampTop(rect.top)
}
let frame = 0
const schedulePlace = () => {
  if (frame) return
  frame = requestAnimationFrame(() => { frame = 0; place() })
}
useEventListener(window, 'resize', schedulePlace)
useEventListener(window, 'scroll', schedulePlace, { capture: true, passive: true })
onUnmounted(() => {
  closed = true
  if (frame) cancelAnimationFrame(frame)
})

defineExpose({ requestClose })
</script>

<style scoped>
.gp {
  position: fixed;
  display: grid;
  width: var(--popover-width);
  max-width: calc(100vw - 16px);
  /* Never taller than the room under the page header (64px) and 8px above the bottom: the list scrolls, the conflict
     buttons stay on screen (7l-6). The dynamic viewport, as window.innerHeight in the placement: on iOS Safari the
     bottom never slips under the browser's toolbar (7p-3). */
  max-height: calc(100dvh - 72px);
  grid-template-rows: auto minmax(0, 1fr) auto;
  border: 1px solid var(--color-line-strong);
  border-radius: var(--radius-lg);
  background: var(--color-bg-raised);
  box-shadow: var(--shadow-popover);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
}
.gp-top {
  display: grid;
  gap: 8px;
  padding: 8px;
}
.gp-search {
  position: relative;
  display: block;
}
.gp-search__icon {
  position: absolute;
  top: 50%;
  left: 9px;
  color: var(--color-text-muted);
  transform: translateY(-50%);
  pointer-events: none;
}
.gp-search__input {
  width: 100%;
  height: 30px;
  padding: 0 10px 0 30px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: var(--color-bg-control);
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
}
.gp-search__input::placeholder {
  color: var(--color-text-muted);
}
.gp-search__input:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -1px;
}
.gp-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-md);
  background: var(--color-bg-seg);
}
.gp-tab {
  min-width: 0;
  max-width: 100%;
  height: 26px;
  flex: 1 1 auto;
  overflow: hidden;
  padding: 0 8px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-medium);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gp-tab[aria-selected='true'] {
  background: var(--color-bg-seg-active);
  color: var(--color-text-primary);
}
@media (hover: hover) {
  .gp-tab:hover:not([aria-selected='true']) {
    background: var(--color-bg-control-hover);
    color: var(--color-text-primary);
  }
}
.gp-tab:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -1px;
}
.gp-list {
  min-height: 0;
  max-height: 252px;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 4px 0;
  border-top: 1px solid var(--color-line-row);
}
.gp-item {
  display: flex;
  height: 36px;
  align-items: center;
  gap: 8px;
  padding: 0 12px;
  cursor: pointer;
}
@media (hover: hover) {
  .gp-item:hover {
    background: var(--color-bg-row-hover);
  }
}
.gp-check {
  width: 14px;
  height: 14px;
  flex: none;
  border: 1px solid var(--color-line-control);
  border-radius: var(--radius-xs);
  background: var(--color-bg-control);
  color: theme('colors.primary.600');
}
.gp-check:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.gp-name {
  min-width: 0;
  flex: 1;
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gp-item.is-checked .gp-name {
  color: var(--color-text-primary);
}
.gp-tag {
  flex: none;
  color: var(--color-text-muted);
  font-size: var(--font-size-micro);
  white-space: nowrap;
}
.gp-empty {
  padding: 20px 12px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  text-align: center;
}
.gp-foot {
  display: flex;
  min-height: 36px;
  align-items: center;
  padding: 0 12px;
  border-top: 1px solid var(--color-line-row);
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.gp-conflict {
  display: grid;
  gap: 10px;
  padding: 12px;
  border-top: 1px solid var(--color-line-row);
  border-radius: 0 0 var(--radius-lg) var(--radius-lg);
  background: var(--color-bg-header);
}
.gp-conflict__msg {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: var(--font-size-small);
  line-height: 18px;
}
.gp-conflict__icon {
  flex: none;
  margin-top: 2px;
  color: var(--color-status-back);
}
.gp-conflict__sub {
  display: block;
  color: var(--color-text-secondary);
}
.gp-conflict__acts {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
/* Touch: rows, tabs, the search and the conflict buttons at least 40px high. */
@media (pointer: coarse) {
  .gp-item,
  .gp-tab,
  .gp-search__input,
  .gp-conflict__acts .btn {
    height: 40px;
  }
}
</style>
