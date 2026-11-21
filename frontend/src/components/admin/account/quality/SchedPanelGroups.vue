<template>
  <section class="qp-section" data-qp-section="groups">
    <h3 class="qp-h">
      <span class="inline-flex items-center gap-1.5">{{ t('admin.schedQuality.drawer.byGroup') }}<SchedPauseMark :global-enabled="detail.global_enabled" :stale="detail.stale" /></span>
      <span class="qp-h-note" data-qp-perf-note>
        {{ note }}<template v-if="perfFailed">
          · <span class="qp-fail" data-qp-perf-failed>{{ t('admin.schedQuality.perf.refreshFailed') }}</span>
          <button v-if="!perf" type="button" class="qp-text-btn ml-1.5" @click="retry(SCHED_PANEL_PERF_TASK)">{{ t('admin.schedQuality.action.retry') }}</button>
        </template>
      </span>
    </h3>
    <p v-if="!detail.groups.length" class="qp-note" data-test="qp-no-groups">{{ t('admin.schedQuality.panel.noGroups') }}</p>
    <div v-else class="gt" role="table" :aria-label="t('admin.schedQuality.drawer.byGroup')">
      <div class="gt__row gt__row--head" role="row">
        <span role="columnheader" class="sr-only">{{ t('admin.schedQuality.col.group') }}</span>
        <span role="columnheader" class="gt__place">{{ t('admin.schedQuality.col.place') }}</span>
        <span v-for="col in COLS" :key="col" v-tip="t(`admin.schedQuality.panel.colTip.${col}`)" role="columnheader" class="gt__n" :class="`gt__n--${col}`">
          {{ t(`admin.schedQuality.panel.col.${col}`) }}
        </span>
        <span role="columnheader" class="sr-only">{{ t('admin.accounts.columns.actions') }}</span>
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
          <span class="gt__title">{{ item.group.name }}</span>
        </span>
        <span class="gt__place" role="cell" data-test="qp-state" :data-qp-place="item.place">
          <StatusDot :tone="item.tone" />
          <span
            v-if="item.gateOff"
            v-tip="t('admin.schedQuality.identity.gateOffGroup')"
            class="qp-muted"
            tabindex="0"
            data-qp-gate-off
          >{{ t('admin.schedQuality.panel.gateOff') }}</span>
          <span v-else>{{ item.placeText }}</span>
        </span>
        <span
          v-for="cell in item.cells"
          :key="cell.col"
          class="gt__n"
          :class="[`gt__n--${cell.col}`, cell.value === null ? 'is-none' : `is-${cell.identity}`]"
          role="cell"
          :aria-label="cell.label"
          :data-qp-cell="cell.col"
          :data-identity="cell.identity"
        >{{ cell.text }}</span>
        <span class="gt__op" role="cell">
          <span v-if="failedGroups.has(item.group.group_id)" class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
          <template v-if="canManage && item.place !== 'stopped'">
            <span v-if="item.group.exemption?.group_id === 0" class="qp-muted">{{ t('admin.schedQuality.panel.allExempt') }}</span>
            <button
              v-else-if="item.group.exemption"
              type="button"
              class="qp-text-btn"
              :aria-label="`${t('admin.schedQuality.action.undoExempt')} ${item.group.name}`"
              :data-qp-trigger="`unexempt:${item.group.group_id}`"
              @click="emit('unexempt', item.group)"
            >{{ t('admin.schedQuality.action.undoExempt') }}</button>
            <button
              v-else
              type="button"
              class="qp-text-btn"
              :aria-label="`${t('admin.schedQuality.action.exempt')} ${item.group.name}`"
              :data-qp-trigger="`exempt:${item.group.group_id}`"
              @click="emit('exempt', item.group, $event.currentTarget as HTMLElement)"
            >{{ t('admin.schedQuality.action.exempt') }}</button>
          </template>
        </span>
        <ul v-if="item.reasons.length" class="gt__why" role="cell">
          <li v-for="reason in item.reasons" :key="reason.dim" :data-qp-why="reason.dim">
            <span class="gt__why-k" :class="`is-${reason.identity}`">{{ t(`admin.schedQuality.dim.${reason.dim}`) }}</span>
            <span>{{ reason.text }}</span>
          </li>
        </ul>
      </div>
    </div>
    <p v-if="detail.groups.length" class="gt__legend" data-qp-legend>
      {{ t('admin.schedQuality.panel.legend') }}
      <span v-for="identity in LEGEND" :key="identity" :class="`is-${identity}`">{{ t(`admin.schedQuality.identity.${identity}`) }}</span>
    </p>
  </section>
</template>

<script setup lang="ts">
/**
 * The quality panel's groups table for the selected model: per group its name, its place (前排, 后排至 / 试用至 the
 * time it ends, 豁免, 停调, or the gate off) and the last 12 hours' requests, slow first output share, error share and
 * cache share; each share is coloured by its line's verdict now (red rear, blue trial, green pass, grey unknown), so
 * the numbers say which line holds the group back (a dash, no figure, stays grey). A group at the rear or on trial says why in one line per line that
 * holds it there: the figures that judgement was taken on (not the 12-hour ones beside them) and when it ends. The
 * whole evidence is in the decision records. A group takes two lines, its name whole on the first.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusDot, { type StatusDotTone } from '@/components/common/StatusDot.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import type { BackendAccountDetail, BackendDetailGroup, BackendDim, BackendIdentity } from '@/api/admin/schedQualityTypes'
import { SCHED_PANEL_PERF_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { DASH, schedWord } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'
import SchedPauseMark from './SchedPauseMark.vue'
import { gateOff, groupPlace, modelRow, placeEnd, placeReasons, type LineReason, type PanelPlace } from './qualityPanel'
import { fmtCount, fmtPct, MIN_ATTEMPTS, perfView } from './performance'

const props = defineProps<{
  detail: BackendAccountDetail
  model: string | null
  targetGroupId: number | null
  /** Name and alias of a group by id (the groups store); the detail's name when unknown. */
  groupInfo: (id: number) => { name: string; alias?: string | null } | undefined
  canManage: boolean
  failedGroups: Set<number>
}>()
const emit = defineEmits<{ exempt: [group: BackendDetailGroup, anchor: HTMLElement]; unexempt: [group: BackendDetailGroup] }>()
const { t, te } = useI18n()
const { panelPerf, retry } = useSchedLayerData()

type Col = 'requests' | BackendDim
const COLS: Col[] = ['requests', 'ttfo', 'error', 'cache']
const LEGEND: BackendIdentity[] = ['pass', 'rear', 'trial', 'unknown']
const PLACE_TONES: Record<PanelPlace, StatusDotTone> = { front: 'ok', trial: 'info', rear: 'back', exempt: 'exempt', stopped: 'off' }

const perf = computed(() => (panelPerf.id !== null && panelPerf.id === props.detail.account.id ? panelPerf.data : null))
const perfFailed = computed(() => panelPerf.failedId !== null && panelPerf.failedId === props.detail.account.id)

/** The 12-hour window and its freshness, by the heading; a window still filling says how much it covers. */
const note = computed(() => {
  const data = perf.value
  if (!data) return perfFailed.value ? t('admin.schedQuality.panel.window') : t('admin.schedQuality.panel.windowLoading')
  if (!data.at) return t('admin.schedQuality.panel.windowLoading')
  const hours = Math.max(0, (Date.parse(data.at) - Date.parse(data.window_from)) / 3_600_000)
  const window = data.complete ? t('admin.schedQuality.panel.window') : t('admin.schedQuality.panel.windowPartial', { hours: hours.toFixed(1) })
  return `${window} · ${t('admin.schedQuality.perf.updated', { time: formatShortTime(data.at) })}`
})

/** The place in words, with the time it ends. */
function placeText(place: PanelPlace, group: BackendDetailGroup, end: string | null): string {
  if (place === 'rear' || place === 'trial') {
    return end ? t(`admin.schedQuality.panel.placeUntil.${place}`, { time: formatShortTime(end) }) : schedWord('placement', place, t, te)
  }
  if (place === 'exempt') {
    const expires = group.exemption?.expires_at
    return expires ? t('admin.schedQuality.panel.placeUntil.exempt', { time: formatShortTime(expires) }) : t('admin.schedQuality.panel.exemptForever')
  }
  return schedWord('placement', place, t, te)
}

function reasonText(reason: LineReason): string {
  const parts: string[] = []
  if (reason.identity === 'trial') parts.push(t('admin.schedQuality.panel.why.trial', { time: reason.end ? formatShortTime(reason.end) : DASH }))
  else {
    if (reason.figures) parts.push(t(`admin.schedQuality.panel.why.${reason.dim}`, reason.figures))
    if (reason.elsewhere) parts.push(t('admin.schedQuality.panel.why.elsewhere'))
    if (reason.end) parts.push(t('admin.schedQuality.panel.why.recheck', { time: formatShortTime(reason.end) }))
  }
  return parts.join(t('admin.schedQuality.panel.why.joiner'))
}

const rows = computed(() => {
  const data = perf.value
  return props.detail.groups.map((group) => {
    const place = groupPlace(props.detail, group, props.model)
    const row = modelRow(group, props.model)
    const end = placeEnd(row, place)
    const perfRow = data?.rows.find((item) => item.account_id === props.detail.account.id && item.group_id === group.group_id && item.model === props.model)
    const view = perfRow ? perfView(perfRow, data?.groups.find((item) => item.group_id === group.group_id)) : null
    const shares: Record<BackendDim, number | null> = {
      ttfo: view?.slowRate ?? null,
      error: view && view.row.attempts.n >= MIN_ATTEMPTS ? view.row.attempts.failures / view.row.attempts.n : null,
      cache: view?.cacheRate ?? null
    }
    const cells = COLS.map((col) => {
      const identity: BackendIdentity | 'plain' = col === 'requests' ? 'plain' : (row?.lines[col].identity ?? 'unknown')
      const value = col === 'requests' ? (view ? view.row.requests : null) : shares[col]
      const text = value === null ? DASH : col === 'requests' ? fmtCount(value) : fmtPct(value)
      const verdict = identity === 'plain' ? '' : schedWord('identity', identity, t, te)
      return { col, identity, value, text, label: [t(`admin.schedQuality.panel.col.${col}`), text, verdict].filter(Boolean).join(' ') }
    })
    const reasons = placeReasons(row, place).map((reason) => ({ ...reason, text: reasonText(reason) }))
    return {
      group,
      info: props.groupInfo(group.group_id) ?? { name: group.name },
      place,
      tone: PLACE_TONES[place],
      placeText: placeText(place, group, end),
      gateOff: place === 'front' && gateOff(props.detail, group) === 'group',
      cells,
      reasons
    }
  })
})
</script>

<style scoped>
@reference "../../../../style.css";

/* The panel's heading rule is scoped to the panel and does not reach here: the title and its note stand apart and the
   note wraps under the title on a narrow drawer. */
.qp-h {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 4px 8px;
  font-size: var(--font-size-body);
  font-weight: var(--font-weight-semibold);
  line-height: var(--line-height-body);
}
.qp-h-note {
  min-width: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  font-weight: var(--font-weight-regular);
}

/* A group: its name whole on the first line (the action at its end), the place and the four figures under it in the
   heading's columns, then the reasons. */
.gt {
  display: grid;
}
.gt__row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) repeat(4, 56px);
  grid-template-areas:
    'name name name name op'
    'place requests ttfo error cache'
    'why why why why why';
  align-items: center;
  column-gap: 8px;
  margin: 0 -8px;
  padding: 8px;
  border-top: 1px solid var(--color-line-row);
  border-radius: var(--radius-sm);
  font-variant-numeric: tabular-nums;
}
.gt__row > * {
  min-width: 0;
}
.gt__row--head {
  grid-template-areas: 'place requests ttfo error cache';
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
.gt__op {
  display: flex;
  grid-area: op;
  align-items: center;
  justify-content: flex-end;
  gap: 6px;
  white-space: nowrap;
}
.gt__place {
  display: flex;
  grid-area: place;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.gt__row:not(.gt__row--head) .gt__place {
  margin-top: 4px;
}
.gt__n {
  text-align: right;
  white-space: nowrap;
}
.gt__row:not(.gt__row--head) .gt__n {
  margin-top: 4px;
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
/* A share takes its line's verdict: red rear, blue trial, green pass, grey unknown; requests stay plain. */
.is-rear {
  color: var(--color-status-error-text);
}
.is-trial {
  color: var(--color-status-info-text);
}
.is-pass {
  color: var(--color-status-ok-text);
}
.is-unknown {
  color: var(--color-text-secondary);
}
/* No figure, no colour: the dash reads the same whatever the verdict (the reason line says why). */
.gt__n.is-none {
  color: var(--color-text-muted);
}
.gt__why {
  display: grid;
  grid-area: why;
  gap: 2px;
  margin: 6px 0 0;
  padding: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-small);
  list-style: none;
}
.gt__why li {
  display: flex;
  gap: 8px;
}
.gt__why-k {
  flex: none;
  font-weight: var(--font-weight-medium);
}
.gt__legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
}

/* A phone: narrower figures, so the place keeps its words. */
@media (max-width: 439px) {
  .gt__row {
    grid-template-columns: minmax(0, 1fr) repeat(4, 48px);
    column-gap: 6px;
  }
}
</style>
