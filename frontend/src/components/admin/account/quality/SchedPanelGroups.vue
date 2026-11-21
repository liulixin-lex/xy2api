<template>
  <p v-if="!detail.groups.length" class="qp-note" data-test="qp-no-groups">{{ t('admin.schedQuality.panel.noGroups') }}</p>
  <div v-else class="gt" role="table" :aria-label="windowText" data-qp-section="groups">
    <div class="gt__row gt__row--head" role="row">
      <span role="columnheader" class="gt__window">
        <span v-tip="updated" :tabindex="updated ? 0 : undefined" class="gt__window-text" data-qp-perf-note>{{ windowText }}</span>
        <SchedPauseMark :global-enabled="detail.global_enabled" :stale="detail.stale" />
        <button
          v-if="perfFailed"
          v-tip="t('admin.schedQuality.perf.refreshFailed')"
          type="button"
          class="gt__retry"
          :aria-label="t('admin.schedQuality.action.retry')"
          data-qp-perf-retry
          @click="retry(SCHED_PANEL_PERF_TASK)"
        ><Icon name="refresh" size="xs" /></button>
      </span>
      <span v-for="col in COLS" :key="col" role="columnheader" class="gt__n" :class="`gt__n--${col}`">{{ t(`admin.schedQuality.panel.col.${col}`) }}</span>
    </div>
    <div
      v-for="item in rows"
      :key="item.group.group_id"
      class="gt__row"
      :class="{ 'is-target': item.group.group_id === targetGroupId }"
      role="row"
      :data-qp-group-row="item.group.group_id"
    >
      <span class="gt__name" role="rowheader">
        <GroupAliasPill :group="item.info" />
        <!-- A name the pill already shows whole is not written twice. -->
        <span v-if="!item.pillIsName" class="gt__title">{{ item.group.name }}</span>
        <span v-else class="sr-only">{{ item.group.name }}</span>
      </span>
      <span class="gt__place" role="cell" data-test="qp-state" :data-qp-place="item.place">
        <span class="gt__place-main">
          <!-- The place is the group's own control: it opens the group's exemption (or its removal) where it stands. -->
          <button
            v-if="item.actionable"
            type="button"
            class="gt__place-btn"
            aria-haspopup="dialog"
            :aria-expanded="openGroupId === item.group.group_id"
            :aria-label="[item.group.name, item.placeText, item.stay].filter(Boolean).join(' ')"
            :data-qp-place-trigger="item.group.group_id"
            @click="emit('place', item.group, $event.currentTarget as HTMLElement)"
          >
            <StatusDot :tone="item.tone" />
            <span>{{ item.placeText }}</span>
            <Icon name="chevronDown" size="xs" :stroke-width="2.5" class="gt__caret" />
          </button>
          <span
            v-else
            v-tip="item.gateOff ? t('admin.schedQuality.identity.gateOffGroup') : ''"
            class="gt__place-static"
            :class="{ 'is-off': item.gateOff }"
            :tabindex="item.gateOff ? 0 : undefined"
            :data-qp-gate-off="item.gateOff || undefined"
          >
            <StatusDot :tone="item.tone" />
            <span>{{ item.placeText }}</span>
          </span>
          <span v-if="failedGroups.has(item.group.group_id)" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
        </span>
        <span v-if="item.stay" class="gt__stay" data-qp-stay>{{ item.stay }}</span>
      </span>
      <span
        v-for="cell in item.cells"
        :key="cell.col"
        class="gt__n"
        :class="[`gt__n--${cell.col}`, cell.value === null && !cell.held ? 'is-none' : `is-${cell.identity}`]"
        role="cell"
        :aria-label="cell.label"
        :data-qp-cell="cell.col"
        :data-identity="cell.identity"
      >
        <span>{{ cell.text }}</span>
        <span v-if="cell.reference" class="gt__ref" data-qp-ref>{{ cell.reference }}</span>
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The quality panel's groups table for the selected model; the layout carries the meaning, not labels:
 * - The first heading is the window ("近 12 小时", or what a filling window covers); when it was worked out is in its tip.
 * - Each group takes two lines: its alias and whole name (once, when the pill already reads it whole), then its place
 *   and the four 12-hour figures. The place reads
 *   前排, 后排, 试用, 豁免至 the time it ends, 停调, or 门槛关; its dot and the figures share one set of colours
 *   (amber rear, blue trial, green front or pass, purple exempt, grey unknown or no figure), so the amber figure is the
 *   line that holds the group at the rear.
 * - Under 后排 its step and when it is reviewed, under 试用 when it started and when it ends at the latest, small like
 *   the figures' references: of the line that ends last, named by its column when more than one line holds the place.
 * - Under a share at the rear or on trial, only what it is measured against, in the same 12-hour terms: the group's
 *   other accounts on the same requests for the first output, the group's limit and minimum for errors and cache.
 * - The place is a button for a manager: it opens the group's exemption, or its removal when exempt, at the place.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import type { BackendAccountDetail, BackendDetailGroup, BackendDim, BackendGroupModel, BackendIdentity } from '@/api/admin/schedQualityTypes'
import { SCHED_PANEL_PERF_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { DASH, schedWord } from '@/utils/schedQualityFormat'
import { groupPillText } from '@/utils/groupAlias'
import { vTip } from '@/utils/tip'
import SchedPauseMark from './SchedPauseMark.vue'
import { gateOff, groupPlace, lineStay, modelRow, placeLine, rearStay, trialStay, type PanelPlace } from './qualityPanel'
import { fmtCount, fmtPct, MIN_ATTEMPTS, perfView } from './performance'

const props = defineProps<{
  detail: BackendAccountDetail
  model: string | null
  targetGroupId: number | null
  /** Name and alias of a group by id (the groups store); the detail's name when unknown. */
  groupInfo: (id: number) => { name: string; alias?: string | null } | undefined
  canManage: boolean
  failedGroups: Set<number>
  /** The group whose place has its popover open. */
  openGroupId: number | null
}>()
const emit = defineEmits<{ place: [group: BackendDetailGroup, anchor: HTMLElement] }>()
const { t, te } = useI18n()
const { panelPerf, retry } = useSchedLayerData()

type Col = 'requests' | BackendDim
const COLS: Col[] = ['requests', 'ttfo', 'error', 'cache']
const PLACE_TONES: Record<PanelPlace, StatusDotTone> = { front: 'ok', trial: 'info', rear: 'back', exempt: 'exempt', stopped: 'off' }

const perf = computed(() => (panelPerf.id !== null && panelPerf.id === props.detail.account.id ? panelPerf.data : null))
const perfFailed = computed(() => panelPerf.failedId !== null && panelPerf.failedId === props.detail.account.id)

/** The window as the first heading: 12 hours, or the hours a filling window covers so far. */
const windowText = computed(() => {
  const data = perf.value
  if (!data?.at || data.complete) return t('admin.schedQuality.panel.window')
  const hours = Math.max(0, (Date.parse(data.at) - Date.parse(data.window_from)) / 3_600_000)
  return t('admin.schedQuality.panel.windowPartial', { hours: hours.toFixed(1) })
})
const updated = computed(() => (perf.value?.at ? t('admin.schedQuality.perf.updated', { time: formatShortTime(perf.value.at) }) : ''))

/** A threshold as set: 0.05 → "5%", 0.025 → "2.5%". */
const limitPct = (ratio: number) => `${Number((ratio * 100).toFixed(1))}%`

function placeText(place: PanelPlace, group: BackendDetailGroup): string {
  if (place === 'exempt') {
    const expires = group.exemption?.expires_at
    return expires ? t('admin.schedQuality.panel.placeUntil.exempt', { time: formatShortTime(expires) }) : t('admin.schedQuality.panel.exemptForever')
  }
  return schedWord('placement', place, t, te)
}

/** The stay under a rear or trial place, of the line that ends last; named by its column when several hold the place. */
function stayText(row: BackendGroupModel | undefined, place: PanelPlace): string {
  const shown = placeLine(row, place)
  if (!shown) return ''
  const { dim, line, held } = shown
  const stay = place === 'rear' ? rearStay(t, line.level, line.until) : trialStay(t, line.trial_started_at, line.trial_until)
  return held > 1 ? lineStay(t, t(`admin.schedQuality.panel.col.${dim}`), stay) : stay
}

const rows = computed(() => {
  const data = perf.value
  return props.detail.groups.map((group) => {
    const info = props.groupInfo(group.group_id) ?? { name: group.name }
    const place = groupPlace(props.detail, group, props.model)
    const row = modelRow(group, props.model)
    const off = place === 'front' && gateOff(props.detail, group) === 'group'
    const perfRow = data?.rows.find((item) => item.account_id === props.detail.account.id && item.group_id === group.group_id && item.model === props.model)
    // A group with its gate off is not judged: an older snapshot's figures for it are not shown.
    const view = perfRow && !off ? perfView(perfRow, data?.groups.find((item) => item.group_id === group.group_id)) : null
    const shares: Record<BackendDim, number | null> = {
      ttfo: view?.slowRate ?? null,
      error: view && view.row.attempts.n >= MIN_ATTEMPTS ? view.row.attempts.failures / view.row.attempts.n : null,
      cache: view?.cacheRate ?? null
    }
    // What a share at the rear or on trial is measured against, in the same 12-hour terms.
    const references: Record<BackendDim, string | null> = {
      ttfo: view?.expectedEnough && view.expectedRate !== null ? t('admin.schedQuality.panel.ref.ttfo', { v: fmtPct(view.expectedRate) }) : null,
      error: t('admin.schedQuality.panel.ref.error', { v: limitPct(group.policy.max_error_rate) }),
      cache: t('admin.schedQuality.panel.ref.cache', { v: limitPct(group.policy.min_cache_ratio) })
    }
    const cells = COLS.map((col) => {
      const identity: BackendIdentity | 'plain' = col === 'requests' ? 'plain' : (row?.lines[col].identity ?? 'unknown')
      const value = col === 'requests' ? (view ? view.row.requests : null) : shares[col]
      const text = value === null ? DASH : col === 'requests' ? fmtCount(value) : fmtPct(value)
      // The line that holds the place keeps its colour even without a 12-hour figure (its evidence is other groups').
      const held = col !== 'requests' && (identity === 'rear' || identity === 'trial')
      const reference = held && value !== null ? references[col as BackendDim] : null
      const verdict = identity === 'plain' ? '' : schedWord('identity', identity, t, te)
      return { col, identity, value, held, text, reference, label: [t(`admin.schedQuality.panel.col.${col}`), text, verdict, reference].filter(Boolean).join(' ') }
    })
    return {
      group,
      info,
      pillIsName: groupPillText(info) === group.name,
      place,
      tone: off ? ('off' as const) : PLACE_TONES[place],
      placeText: off ? t('admin.schedQuality.panel.gateOff') : placeText(place, group),
      stay: off ? '' : stayText(row, place),
      gateOff: off,
      actionable: props.canManage && place !== 'stopped' && !off,
      cells
    }
  })
})
</script>

<style scoped>
@reference "../../../../style.css";

/* A group: its name whole on the first line, the place and the four figures under it in the heading's columns. */
.gt {
  display: grid;
}
.gt__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) repeat(4, 60px);
  grid-template-areas:
    'name name name name name'
    'place requests ttfo error cache';
  align-items: start;
  column-gap: 8px;
  row-gap: 4px;
  margin: 0 -8px;
  padding: 10px 8px;
  border-top: 1px solid var(--color-line-row);
  border-radius: var(--radius-sm);
  font-variant-numeric: tabular-nums;
}
.gt__row > * {
  min-width: 0;
}
.gt__row--head {
  grid-template-areas: 'place requests ttfo error cache';
  align-items: center;
  padding-top: 0;
  padding-bottom: 6px;
  border-top: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.gt__row.is-target {
  border-top-color: transparent;
  background: var(--color-bg-target);
}
.gt__row.is-target + .gt__row {
  border-top-color: transparent;
}
.gt__window {
  display: flex;
  grid-area: place;
  align-items: center;
  gap: 6px;
}
.gt__window-text {
  border-radius: var(--radius-sm);
}
.gt__window-text:focus-visible,
.gt__retry:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.gt__retry {
  display: grid;
  width: 20px;
  height: 20px;
  place-items: center;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-status-error-text);
}
.gt__name {
  display: flex;
  grid-area: name;
  align-items: center;
  gap: 8px;
  font-weight: var(--font-weight-medium);
}
.gt__title {
  overflow-wrap: anywhere;
}
.gt__place {
  display: grid;
  grid-area: place;
  justify-items: start;
}
.gt__place-main {
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
/* The stay under the place: small, like a figure's reference beneath it; on a phone it wraps at its dots only. */
.gt__stay {
  color: var(--color-text-secondary);
  font-size: 11px;
  line-height: 14px;
  word-break: keep-all;
}
/* The place: a quiet button whose dot lines up with the name above; hover and focus show it opens something. */
.gt__place-btn,
.gt__place-static {
  display: inline-flex;
  height: 26px;
  align-items: center;
  gap: 6px;
  padding: 0 6px 0 8px;
  margin-left: -8px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-primary);
  font: inherit;
}
.gt__place-btn {
  cursor: pointer;
  transition: background-color var(--motion-fast) var(--ease-out), border-color var(--motion-fast) var(--ease-out);
}
.gt__caret {
  color: var(--color-text-muted);
  transition: color var(--motion-fast) var(--ease-out), transform var(--motion-fast) var(--ease-out);
}
@media (hover: hover) {
  .gt__place-btn:hover {
    border-color: var(--color-line-control);
    background: var(--color-bg-control-hover);
  }
  .gt__place-btn:hover .gt__caret {
    color: var(--color-text-primary);
  }
}
.gt__place-btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.gt__place-btn:active {
  background: var(--color-bg-seg-active);
}
.gt__place-btn:active .gt__caret {
  transform: translateY(1px);
}
.gt__place-btn[aria-expanded='true'] {
  border-color: var(--color-line-control);
  background: var(--color-bg-control-hover);
}
.gt__place-static.is-off {
  color: var(--color-text-muted);
}
.gt__place-static.is-off:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.gt__n {
  display: grid;
  justify-items: end;
  text-align: right;
  white-space: nowrap;
}
.gt__row:not(.gt__row--head) .gt__n {
  line-height: 26px;
}
.gt__n--requests {
  grid-area: requests;
}
.gt__n--ttfo {
  grid-area: ttfo;
}
.gt__n--error {
  grid-area: error;
}
.gt__n--cache {
  grid-area: cache;
}
/* What a held share is measured against: small, under it, in the plain text colour of a note. */
.gt__ref {
  color: var(--color-text-secondary);
  font-size: 11px;
  line-height: 14px;
}
/* One set of colours with the place's dot: amber rear, blue trial, green pass, grey unknown. A dash is grey, except on
   the line that holds the group at the rear or on trial: it keeps that colour, so the row still shows which line. */
.is-rear {
  color: var(--color-status-back-text);
}
.is-trial {
  color: var(--color-status-info-text);
}
.is-pass {
  color: var(--color-status-ok-text);
}
.is-unknown,
.is-none {
  color: var(--color-text-muted);
}

/* A phone: narrower figures, so the place keeps its words. */
@media (max-width: 439px) {
  .gt__row {
    grid-template-columns: minmax(0, 1fr) repeat(4, 50px);
    column-gap: 6px;
  }
}
/* Touch: the place reaches 40px high with an invisible area inside its row. */
@media (pointer: coarse) {
  .gt__place-btn {
    position: relative;
  }
  .gt__place-btn::after {
    position: absolute;
    inset: -7px 0;
    content: '';
  }
}
</style>
