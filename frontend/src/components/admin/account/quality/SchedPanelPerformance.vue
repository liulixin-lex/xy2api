<template>
  <section class="qp-section" data-qp-section="perf">
    <h3 class="qp-h">
      <!-- The 12-hour snapshot's own freshness: it is refreshed apart from the account's detail. -->
      <span class="inline-flex items-center gap-1.5">{{ t('admin.schedQuality.perf.panel.title') }}<SchedPauseMark v-if="perf" :global-enabled="perf.global_enabled" :stale="perf.stale" /></span>
      <span class="qp-h-note" data-qp-perf-note>
        {{ note }}<template v-if="failed && perf"> · <span class="qp-fail" data-qp-perf-refresh-failed>{{ t('admin.schedQuality.perf.refreshFailed') }}</span></template>
      </span>
    </h3>
    <div v-if="!perf && !failed" class="prows__loading" data-qp-perf-loading>
      <span v-for="i in 3" :key="i" class="skeleton block h-3.5 w-full" />
    </div>
    <div v-else-if="!perf" class="prows__failed" data-qp-perf-failed>
      <span class="qp-fail">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="qp-text-btn" @click="retry(SCHED_PANEL_PERF_TASK)">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <p v-else-if="loadingStats" class="qp-note" data-qp-perf-loading-stats>{{ t('admin.schedQuality.perf.loadingStats') }}</p>
    <p v-else-if="!rows.length" class="qp-note" data-qp-perf-none>{{ t('admin.schedQuality.perf.panel.none') }}</p>
    <div v-else class="prows" role="table" :aria-label="t('admin.schedQuality.perf.panel.title')">
      <div class="prow prow--head" role="row">
        <span role="columnheader">{{ t('admin.schedQuality.perf.col.group') }}</span>
        <span role="columnheader" class="prow__r" :title="t('admin.schedQuality.perf.col.requests')">{{ t('admin.schedQuality.perf.col.requestsShort') }}</span>
        <span role="columnheader" class="prow__r prow__slow">{{ t('admin.schedQuality.perf.col.slowShort') }}</span>
        <span role="columnheader" class="prow__r">{{ t('admin.schedQuality.perf.col.excess') }}</span>
        <span role="columnheader" class="prow__r">{{ t('admin.schedQuality.perf.col.cacheShort') }}</span>
        <span role="columnheader" class="prow__r" :title="t('admin.schedQuality.perf.col.switches')">{{ t('admin.schedQuality.perf.col.switchesShort') }}</span>
      </div>
      <template v-for="item in rows" :key="item.key">
        <div
          class="prow"
          :class="{ 'is-target': item.groupId === targetGroupId, 'prow--none': !item.view }"
          :title="item.title"
          role="row"
          :data-qp-perf-row="item.key"
        >
          <span class="prow__group" role="cell">
            <button
              v-if="item.view"
              type="button"
              class="prow__toggle"
              :aria-expanded="expanded.has(item.key)"
              :aria-controls="`${detailIdPrefix}-${item.key}`"
              :aria-label="t('admin.schedQuality.perf.expand', { name: item.name })"
              :data-qp-perf-expand="item.key"
              @click="toggle(item.key)"
            >
              <Icon name="chevronRight" size="xs" :stroke-width="2.5" />
            </button>
            <span v-else class="prow__toggle" aria-hidden="true" />
            <GroupAliasPill :group="item.info" />
            <span class="prow__name">{{ item.name }}</span>
          </span>
          <!-- A short note that fits the figures' width (268px, 196px on a phone); the tip says why, for a gate that was off
               when the snapshot was made and for a group that joined after it alike. -->
          <span
            v-if="item.gateOff"
            v-tip="t('admin.schedQuality.perf.panel.gateOffTip')"
            class="prow__off qp-muted"
            role="cell"
            tabindex="0"
            :aria-label="`${t('admin.schedQuality.perf.panel.gateOff')} · ${t('admin.schedQuality.perf.panel.gateOffTip')}`"
            data-qp-perf-gate-off
          >{{ t('admin.schedQuality.perf.panel.gateOff') }}</span>
          <template v-else-if="item.view">
            <span class="prow__r" role="cell" data-qp-perf-cell="requests">{{ fmtCount(item.view.row.requests) }}</span>
            <span class="prow__r prow__slow" role="cell" data-qp-perf-cell="slow">
              <template v-if="item.view.slowRate !== null">{{ fmtPct(item.view.slowRate) }}</template>
              <span v-else v-tip="shortTip" class="qp-muted" tabindex="0">{{ item.view.row.first.slow }}/{{ item.view.row.first.n }}</span>
            </span>
            <span class="prow__r" role="cell" data-qp-perf-cell="excess">
              <span
                v-if="item.view.excessTenths !== null"
                v-tip="flagTip(item.view)"
                :class="item.view.slowMark === 'slow' ? 'prow__red' : item.view.slowMark === 'fast' ? 'prow__fast' : ''"
                :tabindex="item.view.slowMark ? 0 : undefined"
              ><span v-if="item.view.slowMark" class="sr-only">{{ flagTip(item.view) }}</span>{{ fmtTenths(item.view.excessTenths) }}</span>
              <span v-else class="qp-muted">{{ DASH }}</span>
            </span>
            <span class="prow__r" role="cell" data-qp-perf-cell="cache">
              <span
                v-if="item.view.cacheRate !== null"
                v-tip="cacheTip(item.view)"
                :class="{ prow__red: item.view.cacheLow }"
                :tabindex="item.view.cacheLow ? 0 : undefined"
              ><span v-if="item.view.cacheLow" class="sr-only">{{ cacheTip(item.view) }}</span>{{ fmtPct(item.view.cacheRate) }}</span>
              <span v-else v-tip="cacheShortTip" class="qp-muted" tabindex="0">{{ t('admin.schedQuality.perf.turns', { n: fmtCount(item.view.row.cont.turns) }) }}</span>
            </span>
            <span class="prow__r" role="cell" data-qp-perf-cell="switches">{{ fmtCount(item.view.switchTotal) }}</span>
          </template>
          <template v-else>
            <span v-for="cell in 5" :key="cell" :class="['prow__r qp-muted', cell === 2 ? 'prow__slow' : '']" role="cell">{{ DASH }}</span>
          </template>
        </div>
        <div v-if="item.view && expanded.has(item.key)" :id="`${detailIdPrefix}-${item.key}`" class="pdet-row" role="row" :data-qp-perf-detail="item.key">
          <div role="cell">
            <SchedPerfDetail :view="item.view" />
          </div>
        </div>
      </template>
      <p v-if="emptyNote" class="qp-note prows__note" :data-qp-perf-empty="emptyNote.kind">{{ emptyNote.text }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
/**
 * The quality panel's "过去 12 小时" (已定事项 13): the account in each of its groups over the last 12 hours, for the
 * model tab, from GET …/accounts/:id/performance (once a minute while the panel is open). One row per group in the
 * order of "各分组" (a group without traffic is a grey row of dashes), then groups the account has left that still
 * have traffic in the window. Marks as on the board (performance.ts), each group against its own settings; a row opens
 * to SchedPerfDetail. Ranks need the whole group: the board has them, this section does not. Under 440px the slow
 * share goes (it is in the detail).
 *
 * A group with the gate off is not counted (its row says so); a snapshot still loading says so instead of "no
 * requests"; an account with requests on other models only keeps its group rows and says this model had none.
 */
import { computed, reactive, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import GroupAliasPill from '@/components/common/GroupAliasPill.vue'
import type { BackendAccountDetail } from '@/api/admin/schedQualityTypes'
import { SCHED_PANEL_PERF_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { DASH } from '@/utils/schedQualityFormat'
import SchedPauseMark from './SchedPauseMark.vue'
import { vTip } from '@/utils/tip'
import SchedPerfDetail from './SchedPerfDetail.vue'
import { fmtCount, fmtPct, fmtTenths, MIN_FIRST, MIN_FIRST_SESSIONS, MIN_TURNS, MIN_TURN_SESSIONS, modelLabel, perfViews, wilson, type PerfView } from './performance'

const props = defineProps<{
  detail: BackendAccountDetail
  model: string | null
  targetGroupId: number | null
  groupInfo: (id: number) => { name: string; alias?: string | null } | undefined
}>()
const { t } = useI18n()
const { panelPerf, retry } = useSchedLayerData()
const detailIdPrefix = `qp-perf-${useId()}`

const perf = computed(() => (panelPerf.id !== null && panelPerf.id === props.detail.account.id ? panelPerf.data : null))
const failed = computed(() => panelPerf.failedId !== null && panelPerf.failedId === props.detail.account.id)

const expanded = reactive(new Set<string>())
watch(() => props.detail.account.id, () => expanded.clear())
function toggle(key: string) {
  if (expanded.has(key)) expanded.delete(key)
  else expanded.add(key)
}

const note = computed(() => {
  const data = perf.value
  if (!data) return ''
  const time = formatShortTime(data.at)
  const parts = [props.model ? t('admin.schedQuality.perf.panel.note', { model: props.model, time }) : t('admin.schedQuality.perf.panel.noteAll', { time })]
  if (!data.complete) {
    parts.push(coveredHours.value < 0.05
      ? t('admin.schedQuality.perf.windowLoading')
      : t('admin.schedQuality.perf.windowPartial', { hours: coveredHours.value.toFixed(1), from: formatShortTime(data.window_from) }))
  }
  return parts.join(' · ')
})

interface PanelPerfRow {
  key: string
  groupId: number
  name: string
  title: string
  info: { name: string; alias?: string | null }
  view: PerfView | null
  /** The group's gate is off: the backend counts nothing there. */
  gateOff: boolean
}

/** The account's rows over every model (the answer's own). */
const accountRows = computed(() => perf.value?.rows.filter((row) => row.account_id === props.detail.account.id) ?? [])
/** No snapshot yet, or one still loading the window with nothing for the account yet. */
const loadingStats = computed(() => !!perf.value && (!perf.value.at || (!perf.value.complete && accountRows.value.length === 0)))
/**
 * A group is counted when its gate was on: the snapshot's own setting first (the one it was counted with), else the
 * account's detail (a group without rows is not in the snapshot).
 */
const counted = (groupId: number) => {
  const own = perf.value?.groups.find((g) => g.group_id === groupId)
  return own ? own.gate_enabled : props.detail.groups.find((g) => g.group_id === groupId)?.policy.enabled !== false
}
const coveredHours = computed(() => (perf.value?.at ? Math.max(0, (Date.parse(perf.value.at) - Date.parse(perf.value.window_from)) / 3_600_000) : 0))
/**
 * Under the rows when the model (or the account) has none: "no requests" only for a fully covered window and in the
 * groups counted; a partly covered one says what it covers so far.
 */
const emptyNote = computed(() => {
  const data = perf.value
  if (!data || !rows.value.some((row) => !row.gateOff)) return null
  const model = props.model === null ? null : modelLabel(props.model, t)
  const none = model === null ? accountRows.value.length === 0 : !accountRows.value.some((row) => row.model === props.model)
  if (!none) return null
  if (!data.complete) {
    return model === null ? null : { kind: 'partial', text: t('admin.schedQuality.perf.panel.partialModel', { hours: coveredHours.value.toFixed(1), model }) }
  }
  return model === null || accountRows.value.length === 0
    ? { kind: 'account', text: t('admin.schedQuality.perf.panel.empty') }
    : { kind: 'model', text: t('admin.schedQuality.perf.panel.emptyModel', { model }) }
})

const rows = computed<PanelPerfRow[]>(() => {
  const data = perf.value
  if (!data) return []
  const ofModel = accountRows.value.filter((row) => props.model === null || row.model === props.model)
  const views = perfViews(ofModel, data.groups, { byModel: true, whole: false })
  const nameOf = (id: number) => props.groupInfo(id)?.name ?? data.groups.find((g) => g.group_id === id)?.name ?? props.detail.groups.find((g) => g.group_id === id)?.name ?? `#${id}`
  const item = (groupId: number, view: PerfView | null): PanelPerfRow => {
    const name = nameOf(groupId)
    const label = [name, props.model === null && view ? modelLabel(view.row.model, t) : '', view && !view.row.member ? t('admin.schedQuality.perf.notMember').trim() : ''].filter(Boolean).join(' · ')
    return {
      key: view ? view.key : `${groupId}:none`,
      groupId,
      name: label,
      title: label,
      info: props.groupInfo(groupId) ?? { name },
      view,
      gateOff: !view && !counted(groupId)
    }
  }
  const list: PanelPerfRow[] = []
  const seen = new Set<string>()
  for (const group of props.detail.groups) {
    const mine = views.filter((view) => view.row.group_id === group.group_id)
    if (!mine.length) list.push(item(group.group_id, null))
    for (const view of mine) {
      list.push(item(group.group_id, view))
      seen.add(view.key)
    }
  }
  // Groups the account has left that still have traffic in the window.
  for (const view of views) if (!seen.has(view.key)) list.push(item(view.row.group_id, view))
  return list
})

const shortTip = computed(() => t('admin.schedQuality.perf.shortTip', { min: MIN_FIRST, sessions: MIN_FIRST_SESSIONS }))
const cacheShortTip = computed(() => t('admin.schedQuality.perf.cacheShortTip', { min: MIN_TURNS, sessions: MIN_TURN_SESSIONS }))
function flagTip(view: PerfView): string {
  if (!view.slowMark) return ''
  const bounds = wilson(view.row.first.slow, view.row.first.n)
  const expected = fmtPct(view.expectedRate!)
  return view.slowMark === 'slow'
    ? t('admin.schedQuality.perf.flag.slow', { lower: fmtPct(bounds.lower), expected })
    : t('admin.schedQuality.perf.flag.fast', { upper: fmtPct(bounds.upper), expected })
}
const cacheTip = (view: PerfView) => (view.cacheLow && view.group
  ? t('admin.schedQuality.perf.flag.cacheLow', { rate: fmtPct(view.cacheRate!), min: fmtPct(view.group.min_cache_ratio, 0) })
  : '')
</script>

<style scoped>
/* The panel's heading rule is scoped to the panel and does not reach here: the title and its note stand apart and the
   note wraps under the title on a narrow drawer (as in the decisions heading). */
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
.prows {
  display: grid;
}
.prows__loading {
  display: grid;
  gap: 10px;
}
.prows__failed {
  display: flex;
  align-items: center;
  gap: 10px;
}
.prow {
  display: grid;
  /* Group, requests, slow, excess, cache, switches; two headings are short words (English "Reqs", "Sw."; the whole word
     is their title), so the group name keeps its room. */
  grid-template-columns: minmax(0, 1fr) 52px 48px 52px 56px 44px;
  align-items: center;
  column-gap: 4px;
  min-height: 36px;
  margin: 0 -8px;
  padding: 0 8px;
  border-top: 1px solid var(--color-line-row);
  border-radius: var(--radius-sm);
  font-size: var(--font-size-body);
  font-variant-numeric: tabular-nums;
  line-height: var(--line-height-body);
}
.prow > * {
  min-width: 0;
}
.prow--head {
  min-height: 28px;
  border-top: 0;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
}
.prow.is-target {
  border-top-color: transparent;
  background: var(--color-bg-target);
}
.prow.is-target + .prow {
  border-top-color: transparent;
}
.prow__r {
  overflow: hidden;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prow__group {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}
.prow__name {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prow--none .prow__name {
  color: var(--color-text-muted);
}
.prow__toggle {
  display: grid;
  width: 20px;
  height: 20px;
  flex: none;
  place-items: center;
  margin-left: -4px;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
}
.prow__toggle svg {
  transition: transform var(--motion-fast) ease-out;
}
.prow__toggle[aria-expanded='true'] svg {
  transform: rotate(90deg);
}
.prow__toggle:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: -2px;
}
@media (prefers-reduced-motion: reduce) {
  .prow__toggle svg {
    transition: none;
  }
}
.prow__red {
  color: var(--color-mark-fail);
  font-weight: var(--font-weight-semibold);
}
.prow__fast {
  color: var(--color-mark-pass);
}
/* A group not counted: one note across the figures. */
.prow__off {
  grid-column: 2 / -1;
  overflow: hidden;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prows__note {
  padding-top: 8px;
}
.pdet-row {
  margin: 0 -8px;
  padding: 4px 8px 12px 32px;
}
/* A phone (prototype, panel under 440px): the slow share goes to the detail; five columns fit 288px. */
@media (max-width: 439px) {
  .prow {
    grid-template-columns: minmax(0, 1fr) 44px 48px 52px 40px;
  }
  .prow__slow {
    display: none;
  }
}
</style>
