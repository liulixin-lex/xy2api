<template>
  <!-- Touch (mobile list plan): the whole cell is one button that lists every group in a bottom sheet, each 48px high:
       its dot, name and state; a group with quality data opens the quality panel on it; managers edit the groups from
       the sheet's foot. The pills are drawn as they are, without hit zones of their own. -->
  <button
    v-if="coarse"
    ref="cellRef"
    type="button"
    class="pills pills--touch"
    :class="{ 'pills--center': groups.length <= 3 }"
    :data-groups-cell="account.id"
    :data-quality-opener="openerKey"
    aria-haspopup="dialog"
    :aria-label="t('admin.accounts.table.groupsSheetOpen', { name: account.name, n: groups.length })"
    data-test="group-pills"
    @click="sheetOpen = true"
  >
    <span v-if="!groups.length" class="pill pill--empty" aria-hidden="true" data-test="group-pill-empty"><Icon name="plus" size="xs" /></span>
    <span v-for="pill in shownPills" :key="pill.group.id" class="pill" :class="{ 'pill--nodot': !pill.state }" :data-group-id="pill.group.id" aria-hidden="true" data-test="group-pill">
      <span v-if="pill.state" class="pill__dot"><StatusDot :tone="DOT_TONES[pill.state]" /></span>
      <span class="pill__text" :class="{ 'pill__text--latin': pill.latin }">{{ pill.text }}</span>
    </span>
    <span v-if="rest.length" class="pill pill--more" :class="{ 'pill--nodot': !restWorst }" aria-hidden="true" data-test="group-pill-more">
      <span v-if="restWorst" class="pill__dot"><StatusDot :tone="DOT_TONES[restWorst.state]" /></span>
      <span class="pill__text">+{{ rest.length }}</span>
    </span>
    <BottomSheet :show="sheetOpen" :title="t('admin.accounts.table.groupsSheetTitle', { name: account.name })" test-id="groups-sheet" @close="sheetOpen = false">
      <ul v-if="groups.length" class="gsheet">
        <li v-for="item in sheetItems" :key="item.group.id">
          <button v-if="item.state" type="button" class="gsheet__item" :data-group-id="item.group.id" data-test="groups-sheet-item" @click="openGroup(item.group.id)">
            <StatusDot :tone="DOT_TONES[item.state]" />
            <span class="gsheet__name">{{ item.group.name }}</span>
            <span class="gsheet__state">{{ stateLabel(item.state) }}</span>
          </button>
          <div v-else class="gsheet__item gsheet__item--plain" :data-group-id="item.group.id" data-test="groups-sheet-item">
            <span class="gsheet__name">{{ item.group.name }}</span>
            <span class="gsheet__state">{{ t('admin.accounts.table.pillNoQuality') }}</span>
          </div>
        </li>
      </ul>
      <p v-else class="gsheet__none">{{ t('admin.accounts.table.groupsSheetNone') }}</p>
      <template v-if="!readonly" #footer>
        <button type="button" class="btn btn-secondary" data-test="groups-sheet-edit" @click="editGroups">{{ t('admin.accounts.editGroups') }}</button>
      </template>
    </BottomSheet>
  </button>
  <div v-else class="pills" :class="{ 'pills--center': groups.length <= 3 }" :data-groups-cell="account.id" data-test="group-pills">
    <button
      v-if="!groups.length"
      type="button"
      class="pill pill--empty"
      :disabled="readonly"
      :aria-label="t('admin.accounts.table.pillEmptyLabel', { name: account.name })"
      data-test="group-pill-empty"
      :data-groups-opener="account.id"
      @click="emit('edit-groups', $event)"
    >
      <Icon name="plus" size="xs" />
    </button>

    <template v-for="pill in shownPills" :key="pill.group.id">
      <span
        class="pill"
        :class="{ 'pill--nodot': !pill.state }"
        :data-group-id="pill.group.id"
        data-test="group-pill"
      >
        <button
          v-if="pill.state"
          type="button"
          class="pill__dot"
          :aria-label="t('admin.accounts.table.pillDotLabel', { name: pill.group.name, state: stateLabel(pill.state) })"
          v-tip="quietTip(`pill:${pill.group.id}`, () => pillCard(pill.group, pill.state))"
          :data-quality-opener="qualityOpenerKey(account.id, pill.group.id)"
          data-test="group-pill-dot"
          @click="hush(`pill:${pill.group.id}`); emit('open-quality', pill.group.id)"
        >
          <StatusDot :tone="DOT_TONES[pill.state]" />
        </button>
        <button
          type="button"
          class="pill__text"
          :class="{ 'pill__text--latin': pill.latin }"
          :disabled="readonly"
          :aria-label="t('admin.accounts.table.pillTextLabel', { name: pill.group.name })"
          v-tip="quietTip(`pill:${pill.group.id}`, () => pillCard(pill.group, pill.state))"
          data-test="group-pill-text"
          :data-groups-opener="account.id"
          @click="hush(`pill:${pill.group.id}`); emit('edit-groups', $event)"
        >{{ pill.text }}</button>
      </span>
    </template>

    <span v-if="rest.length" class="pill pill--more" :class="{ 'pill--nodot': !restWorst }" data-test="group-pill-more">
      <button
        v-if="restWorst"
        type="button"
        class="pill__dot"
        :aria-label="t('admin.accounts.table.pillMoreWorst', { n: rest.length, state: stateLabel(restWorst.state) })"
        v-tip="quietTip('more', moreTip)"
        :data-quality-opener="qualityOpenerKey(account.id, restWorst.group.id)"
        @click="hush('more'); emit('open-quality', restWorst.group.id)"
      >
        <StatusDot :tone="DOT_TONES[restWorst.state]" />
      </button>
      <button
        type="button"
        class="pill__text"
        :aria-label="t('admin.accounts.table.pillMoreLabel', { n: rest.length })"
        v-tip="quietTip('more', moreTip)"
        data-test="group-pill-more-text"
        @click="showTipNow($event.currentTarget)"
      >+{{ rest.length }}</button>
    </span>
  </div>
</template>

<script setup lang="ts">
/**
 * Groups column (spec 4.3, prototype): a fixed 3×3 grid of pills in the groups page's order. One to three pills sit in
 * one row, centred; more fill from the top. With more than nine the ninth is "+N" with the worst folded state; hovering
 * or clicking its text lists every folded group (spec 4.3, 7d-7), its dot opens the worst one. Each pill has two hit
 * zones: the dot opens the quality panel on that group, the text edits the account's groups; both show the group's
 * tip card (12.7): its full name and state, and the state's data. Non-OpenAI accounts have no quality data and no dot.
 *
 * On a touch screen (coarse pointer, whatever the width; mobile list plan) 50x20 pills are too small to hit: the whole
 * cell opens a bottom sheet listing every group (its dot, name and state, 48px each); one with quality data opens the
 * quality panel on that group, and managers edit the groups from the sheet's foot (`edit-groups-at`, with the cell as
 * the picker's anchor). Editing also goes through the row menu's "edit groups". A mouse keeps the two zones.
 */
import { computed, nextTick, ref, watch } from 'vue'
import BottomSheet from '@/components/common/BottomSheet.vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import { iqModelLabel } from '@/components/admin/account/iqCheckPresentation'
import { usePointerCoarse } from '@/composables/usePointerCoarse'
import { qualityOpenerKey } from '@/composables/useSchedLayers'
import type { Account, AdminGroup } from '@/types'
import type { BackendDim, BackendRear, BackendTrial } from '@/api/admin/schedQualityTypes'
import { endTime, knownDim, lineStay, rearStay, trialStay } from '@/components/admin/account/quality/qualityPanel'
import { anyOverlayOpen, overlaysClosed } from '@/utils/overlayStack'
import { DASH, kindName } from '@/utils/schedQualityFormat'
import { hideTip, showTipNow, vTip, type TipCard, type TipCell, type TipContent, type TipTone } from '@/utils/tip'
import { groupPillText, isLatinPillText } from '@/utils/groupAlias'
import { groupDotState, splitPills, worstDotState, type AccountQualityView, type GroupDotState } from './accountGroups'
import { formatShortTime } from './accountTime'

const props = defineProps<{
  account: Account
  /** The account's groups, already in the groups page's order. */
  groups: AdminGroup[]
  /** The account's quality summary; null for accounts without quality data. */
  quality: AccountQualityView | null
  readonly?: boolean
  /** Quality scheduling is switched off (a fresh summary's global_enabled; a stale one cannot tell): an unknown dot says so. */
  gateOff?: boolean
}>()
const emit = defineEmits<{
  'open-quality': [groupId: number]
  'edit-groups': [event: MouseEvent]
  /** The touch sheet's "edit groups": the picker opens beside the cell, focus coming back to it. */
  'edit-groups-at': [opener: HTMLElement]
}>()
const { t, te } = useI18n()
const coarse = usePointerCoarse()

// Trial is blue (info); unknown keeps the grey of the former "observing".
const DOT_TONES: Record<GroupDotState, StatusDotTone> = { ok: 'ok', back: 'back', exempt: 'exempt', trial: 'info', unknown: 'observe', stopped: 'off' }
const stateLabel = (state: GroupDotState) => t(`admin.accounts.table.dotStates.${state}`)
const TIP_TONES: Record<GroupDotState, TipTone> = { ok: 'ok', back: 'back', exempt: 'exempt', trial: 'info', unknown: 'observe', stopped: 'stopped' }
const stateCell = (state: GroupDotState): TipCell => ({ text: stateLabel(state), tone: TIP_TONES[state], dot: true })

const stateOf = (group: AdminGroup): GroupDotState | null => (props.quality ? groupDotState(props.quality, group.id) : null)

const split = computed(() => splitPills(props.groups))
const shownPills = computed(() =>
  split.value.shown.map((group) => {
    const text = groupPillText(group)
    return { group, text, latin: isLatinPillText(text), state: stateOf(group) }
  })
)
const rest = computed(() => split.value.rest)

// ---- Touch: the cell's bottom sheet ----
const cellRef = ref<HTMLElement | null>(null)
const sheetOpen = ref(false)
/** The quality panel gives focus back to the element with its opener key: the cell, for the group opened last. */
const openerKey = ref<string | undefined>(undefined)
const sheetItems = computed(() => props.groups.map((group) => ({ group, state: stateOf(group) })))
// The sheet belongs to the touch layout: leaving it (a mouse becomes the main pointer) closes the sheet, so it does not
// open again by itself on the next switch back to touch.
watch(coarse, (touch) => { if (!touch) sheetOpen.value = false })
/** The sheet closes first (focus back on the cell), then the layer it asked for opens. */
const openGroup = async (groupId: number) => {
  openerKey.value = qualityOpenerKey(props.account.id, groupId)
  sheetOpen.value = false
  await nextTick()
  emit('open-quality', groupId)
}
const editGroups = async () => {
  sheetOpen.value = false
  await nextTick()
  if (cellRef.value) emit('edit-groups-at', cellRef.value)
}
const restWorst = computed(() => {
  if (!props.quality) return null
  const states = rest.value.map((group) => ({ group, state: stateOf(group)! }))
  const worst = worstDotState(states.map((item) => item.state))
  return worst ? states.find((item) => item.state === worst)! : null
})

const lineName = (dim: BackendDim) => t(`admin.schedQuality.dim.${dim}`)
const reasonCell = (lines: BackendDim[]): TipCell => ({ text: lines.map((line) => t(`admin.accounts.table.dimReasons.${line}`)).join('、'), tone: 'back' })
/** "第 3 档 · 15:30 复核": the step and review of the line at the back that ends last. */
const rearEnd = (item: BackendRear) => rearStay(t, item.level, item.until)
/** "15:10 起 · 最晚 17:10": when the trial that ends last started, and when it ends at the latest. */
const trialEnd = (item: BackendTrial) => trialStay(t, item.trial_started_at, item.until)
/** A stay with its line named before it, when the answer says which line it is of. */
const namedEnd = (line: unknown, stay: string) => {
  const dim = knownDim(line)
  return dim ? lineStay(t, lineName(dim), stay) : stay
}
/**
 * A model at the back: its line that ends last with the step and review on the first row, its other lines at the back
 * under it. An older answer does not say which line that is: the lines are listed together beside the stay.
 */
function rearRows(item: BackendRear): (string | TipCell)[][] {
  const last = knownDim(item.until_line)
  if (!last) return [[iqModelLabel(item.model), reasonCell(item.lines), rearEnd(item)]]
  return [[iqModelLabel(item.model), reasonCell([last]), rearEnd(item)], ...item.lines.filter((line) => line !== last).map((line) => ['', reasonCell([line]), ''])]
}
/** The group's model that ends last; one with a known end before any without. */
const lastOf = <T extends { group_id: number; until?: string | null }>(items: T[], group: AdminGroup) =>
  items.filter((item) => item.group_id === group.id).reduce<T | undefined>((last, item) => (!last || endTime(item.until) > endTime(last.until) ? item : last), undefined)

const exemptionOf = (quality: AccountQualityView, group: AdminGroup) =>
  quality.exemptions.find((item) => item.group_id === group.id) ?? quality.exemptions.find((item) => item.group_id === 0)
const exemptEnd = (expiresAt: string | null | undefined) => (expiresAt ? formatShortTime(expiresAt) : t('admin.accounts.table.tip.forever'))

/**
 * The rows of the group's state (12.7): a model at the back on the row of its line that ends last, with why, its step
 * and review, its other lines at the back under it; a model on trial with its line, start and latest end; the
 * exemption, …
 */
function stateRows(group: AdminGroup, state: GroupDotState): TipCard['rows'] {
  const quality = props.quality
  if (!quality) return []
  if (state === 'back') {
    return (quality.rear ?? [])
      .filter((item) => item.group_id === group.id)
      .flatMap(rearRows)
  }
  if (state === 'trial') {
    return (quality.trial ?? [])
      .filter((item) => item.group_id === group.id)
      .map((item) => {
        const line = knownDim(item.until_line)
        return [iqModelLabel(item.model), line ? { text: lineName(line), tone: 'info' } : { text: DASH, tone: 'muted' }, trialEnd(item)]
      })
  }
  if (state === 'exempt') {
    const exemption = exemptionOf(quality, group)
    const rows = [[t('admin.accounts.table.tip.exemptUntil'), exemptEnd(exemption?.expires_at)]]
    return exemption?.reason ? [...rows, [t('admin.accounts.table.tip.reason'), exemption.reason]] : rows
  }
  // Unknown may come from any of the three lines: the panel has each line's evidence. With quality scheduling switched
  // off nothing is judged, as the panel says (a group's own gate is not in the summary).
  if (state === 'unknown') {
    return [[t('admin.accounts.table.tip.reason'), t(props.gateOff ? 'admin.schedQuality.identity.gateOffGlobal' : 'admin.accounts.table.tip.unknownWhy')]]
  }
  if (state === 'stopped' && quality.stop) {
    const rows = [
      [t('admin.accounts.table.tip.reason'), kindName(quality.stop.reason, t, te)],
      [t('admin.accounts.table.tip.source'), t(`admin.accounts.table.tip.sources.${quality.stop.source === 'manual' ? 'manual' : 'auto'}`)]
    ]
    return quality.stop.until ? [...rows, [t('admin.accounts.table.tip.resume'), formatShortTime(quality.stop.until)]] : rows
  }
  return []
}

/**
 * A click on a hit zone that opens the quality panel or the group picker puts its tip away at once, and while that
 * layer is open neither zone of the same pill shows the card, the two zones showing the same one (7i-3, 12.12-8); once
 * the layer closes the pill's card is back, even with some other layer open (7m-2). Other pills' cards show as usual.
 */
let quiet: { pill: string; closed: number } | null = null
const hush = (pill: string) => {
  quiet = { pill, closed: overlaysClosed() }
  hideTip()
}
const quietTip = (pill: string, content: () => TipContent) => () => {
  if (quiet && overlaysClosed() !== quiet.closed) quiet = null
  return quiet?.pill === pill && anyOverlayOpen() ? '' : content()
}

/** The group's card: its full name, its state at the top right and the state's rows; no state without quality data. */
function pillCard(group: AdminGroup, state: GroupDotState | null): TipCard {
  if (!state) return { title: group.name, chip: { text: t('admin.accounts.table.pillNoQuality'), tone: 'muted' } }
  return { title: group.name, chip: stateCell(state), rows: stateRows(group, state) }
}

/** "+N": a row per folded group, with its dot and state and, at the back or on trial, the line that ends last, or when its exemption ends. */
function moreTip(): TipCard {
  const title = t('admin.accounts.table.pillMore', { n: rest.value.length })
  if (!props.quality) return { title, chip: { text: t('admin.accounts.table.pillNoQuality'), tone: 'muted' }, rows: rest.value.map((group) => [group.name]) }
  const quality = props.quality
  return {
    title,
    rows: rest.value.map((group) => {
      const state = stateOf(group)!
      const rear = state === 'back' ? lastOf(quality.rear ?? [], group) : undefined
      const trial = state === 'trial' ? lastOf(quality.trial ?? [], group) : undefined
      const end = rear
        ? namedEnd(rear.until_line, rearEnd(rear))
        : trial
          ? namedEnd(trial.until_line, trialEnd(trial))
          : state === 'exempt'
            ? exemptEnd(exemptionOf(quality, group)?.expires_at)
            : ''
      return [group.name, stateCell(state), end]
    })
  }
}
</script>

<style scoped>
@reference "../../../../style.css";

.pills {
  display: grid;
  height: 66px;
  grid-template-columns: repeat(3, var(--pill-width));
  grid-auto-rows: var(--pill-height);
  gap: var(--pill-gap-y) var(--pill-gap-x);
  align-content: start;
}
.pills--center {
  align-content: center;
}
.pill {
  display: flex;
  width: var(--pill-width);
  height: var(--pill-height);
  overflow: hidden;
  border-radius: var(--radius-sm);
  background: var(--color-bg-pill);
  color: var(--color-text-primary);
  font-size: var(--font-size-small);
  line-height: var(--pill-height);
}
.pill__dot,
.pill__text {
  height: 100%;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  transition: background-color var(--motion-fast) var(--ease-out);
}
.pill__dot {
  display: grid;
  flex: none;
  width: var(--pill-dot-zone);
  place-items: center;
  border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  cursor: pointer;
}
.pill__dot :deep(.status-dot) {
  transition: transform var(--motion-fast) var(--ease-out);
}
.pill__text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  padding-right: 4px;
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  font-size: var(--font-size-small);
  text-align: left;
  text-overflow: clip;
  white-space: nowrap;
  cursor: pointer;
}
.pill__text:disabled {
  cursor: default;
}
.pill__text--latin {
  font-size: var(--font-size-small);
}
.pill__dot:focus-visible,
.pill__text:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (hover: hover) {
  .pill__dot:hover {
    background: var(--color-bg-pill-hover);
  }
  .pill__dot:hover :deep(.status-dot) {
    transform: scale(1.3);
  }
  .pill__text:hover:not(:disabled) {
    background: var(--color-bg-pill-hover);
  }
}
.pill--nodot .pill__text {
  padding: 0;
  border-radius: var(--radius-sm);
  text-align: center;
}
.pill--more .pill__text {
  color: var(--color-text-secondary);
}
/* Touch: one button over the whole pill; the text still cuts inside it. */
/* Touch: the cell is one button, at least 44px high, the pills inside drawn as they are. */
.pills--touch {
  width: 100%;
  min-height: 44px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-md);
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.pills--touch:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.pills--touch .pill__dot {
  display: inline-grid;
  place-items: center;
}
.gsheet {
  display: grid;
  margin: 0 -8px;
  padding: 0;
  list-style: none;
}
.gsheet__item {
  display: grid;
  width: 100%;
  min-height: 48px;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding: 0 8px;
  border: 0;
  border-bottom: 1px solid var(--color-line-row);
  border-radius: 0;
  background: transparent;
  color: var(--color-text-primary);
  font-size: var(--font-size-name);
  text-align: left;
}
button.gsheet__item {
  cursor: pointer;
}
.gsheet__item--plain {
  grid-template-columns: minmax(0, 1fr) auto;
}
.gsheet__item:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (hover: hover) {
  button.gsheet__item:hover {
    background: var(--color-bg-control-hover);
  }
}
.gsheet__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gsheet__state {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.gsheet__none {
  margin: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-body);
}
.pill--empty {
  display: grid;
  place-items: center;
  padding: 0;
  border: 1px dashed var(--color-line-control);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
}
.pill--empty:disabled {
  cursor: default;
}
@media (hover: hover) {
  .pill--empty:hover:not(:disabled) {
    border-color: var(--color-text-muted);
    background: var(--color-bg-pill);
    color: var(--color-text-primary);
  }
}
</style>
