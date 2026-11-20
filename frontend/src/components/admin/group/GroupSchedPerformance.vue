<template>
  <div class="gperf" data-sq-perf>
    <div v-if="!perf && !failed" data-sq-perf-loading>
      <span v-for="i in 6" :key="i" :class="['skeleton block h-3.5 w-full', i === 1 ? '' : 'mt-4']" />
    </div>
    <div v-else-if="!perf" class="flex items-center gap-3" data-sq-perf-failed>
      <span class="sq-fail-word text-sm">{{ t('admin.schedQuality.result.failed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" @click="retry(SCHED_BOARD_PERF_TASK)">{{ t('admin.schedQuality.action.retry') }}</button>
    </div>
    <template v-else>
      <p class="gperf__line sq-text-3" data-sq-perf-window>
        <span>{{ windowText }}</span>
        <span v-if="failed" class="sq-fail-word" data-sq-perf-refresh-failed>{{ t('admin.schedQuality.perf.refreshFailed') }}</span>
      </p>
      <!-- A group with the gate off is not counted; a snapshot still loading has nothing yet: neither is "no requests". -->
      <p v-if="gateOff" class="sq-text-3 text-sm" data-sq-perf-gate-off>{{ t('admin.schedQuality.perf.gateOff') }}</p>
      <p v-else-if="loadingStats" class="sq-text-3 text-sm" data-sq-perf-loading-stats>{{ t('admin.schedQuality.perf.loadingStats') }}</p>
      <div v-if="priorities.length > 1 || redCount > 0" class="gperf__filters">
        <div v-if="priorities.length > 1" class="gperf__prio">
          <span class="gperf__prio-label sq-text-3" aria-hidden="true">{{ t('admin.schedQuality.perf.priority') }}</span>
          <div v-roving-tabs role="tablist" :aria-label="t('admin.schedQuality.perf.priority')" class="gperf__tabs" data-sq-perf-priorities>
            <button
              v-for="option in [null, ...priorities]"
              :key="option ?? 'all'"
              type="button"
              role="tab"
              class="gperf__tab"
              :aria-selected="option === priority"
              :tabindex="option === priority ? 0 : -1"
              :data-sq-perf-priority="option ?? 'all'"
              @click="pickPriority(option)"
            >{{ option === null ? t('admin.schedQuality.perf.priorityAll') : option }}</button>
          </div>
        </div>
        <button
          v-if="redCount > 0"
          type="button"
          class="gperf__red"
          :aria-pressed="redOnly"
          data-sq-perf-red
          @click="redOnly = !redOnly"
        >{{ t('admin.schedQuality.perf.redOnly', { n: redCount }) }}</button>
      </div>
      <p v-if="!shown.length && !gateOff && !loadingStats" class="sq-text-3 text-sm" data-sq-perf-empty>{{ emptyText }}</p>
      <div v-if="shown.length" ref="scrollRef" class="-mx-3 overflow-x-auto" :style="{ '--gperf-visible': `${visible}px` }" data-sq-perf-scroll>
        <table class="sq-table gperf__table" data-sq-perf-table>
          <thead>
            <tr>
              <th scope="col" class="gperf__acct">{{ t('admin.schedQuality.perf.col.account') }}</th>
              <th v-for="col in PERF_SORT_KEYS" :key="col" scope="col" class="sq-r" :aria-sort="ariaSort(col)" :data-sq-perf-col="col">
                <button v-tip="colTip(col)" type="button" class="gperf__sort sq-focus" :data-sq-perf-sort="col" @click="sort = nextSort(sort, col)">
                  {{ t(`admin.schedQuality.perf.col.${col}`) }}<span class="gperf__arrow" aria-hidden="true">{{ arrow(col) }}</span>
                </button>
              </th>
              <th scope="col">
                <span v-tip="t('admin.schedQuality.perf.tip.now')" class="sq-focus" tabindex="0">{{ t('admin.schedQuality.perf.col.now') }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <template v-for="view in shown" :key="view.key">
              <tr :class="{ 'sq-weak': view.allShort }" :data-sq-perf-row="view.key">
                <td class="gperf__acct">
                  <div class="gperf__who">
                    <button
                      type="button"
                      class="gperf__toggle"
                      :aria-expanded="expanded.has(view.key)"
                      :aria-controls="`${detailIdPrefix}-${view.key}`"
                      :aria-label="t('admin.schedQuality.perf.expand', { name: view.row.name })"
                      :data-sq-perf-expand="view.key"
                      @click="toggle(view.key)"
                    >
                      <Icon name="chevronRight" size="xs" :stroke-width="2.5" />
                    </button>
                    <div class="gperf__id">
                      <span class="gperf__name" :title="view.row.member ? view.row.name : `${view.row.name}${t('admin.schedQuality.perf.notMember')}`">
                        <button
                          v-if="canViewAccounts"
                          type="button"
                          :class="['sq-link sq-focus', { 'sq-text-3': !view.row.member }]"
                          :data-sq-trigger="`account:${view.row.account_id}`"
                          :data-quality-opener="qualityOpenerKey(view.row.account_id, view.row.group_id)"
                          @click="openSchedDrawer({ id: view.row.account_id, name: view.row.name }, { model: view.row.model, groupId: view.row.group_id })"
                        >{{ view.row.name }}</button>
                        <span v-else :class="{ 'sq-text-3': !view.row.member }">{{ view.row.name }}</span>
                      </span>
                      <!-- An account that left the group says so at the start of the second line, which a long name cannot
                           cut (it sat after the name and went with it on a phone). -->
                      <span class="gperf__sub gperf__model" :title="view.row.model">
                        <span v-if="!view.row.member" class="gperf__left" data-sq-perf-left>{{ t('admin.schedQuality.perf.left') }} · </span>{{ subOf(view) }}
                      </span>
                    </div>
                  </div>
                </td>
                <td class="sq-r sq-num" data-sq-cell="requests">
                  <span class="gperf__main">{{ fmtCount(view.row.requests) }}</span>
                  <span v-if="view.share !== null" class="gperf__sub">{{ fmtPct(view.share) }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="slow">
                  <template v-if="view.slowRate !== null">
                    <span class="gperf__main">{{ fmtPct(view.slowRate) }}</span>
                    <span v-tip="expectedTip(view)" class="gperf__sub" :tabindex="expectedTip(view) ? 0 : undefined">{{ expectedText(view) }}</span>
                  </template>
                  <span v-else v-tip="shortTip" class="gperf__main sq-text-3" tabindex="0">{{ t('admin.schedQuality.perf.slowCount', { slow: fmtCount(view.row.first.slow), n: fmtCount(view.row.first.n) }) }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="excess">
                  <span v-if="view.excessTenths !== null" v-tip="flagTip(view, 'excess')" :class="['gperf__main', markClass(view)]" :tabindex="flagTip(view, 'excess') ? 0 : undefined">
                    <span v-if="flagTip(view, 'excess')" class="sr-only">{{ flagTip(view, 'excess') }}</span>{{ fmtTenths(view.excessTenths) }}
                  </span>
                  <span v-else v-tip="missingTip(view)" class="gperf__main sq-text-3" tabindex="0">{{ DASH }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="extra">
                  <span
                    v-if="view.extra !== null"
                    v-tip="view.slowMark === 'slow' ? flagTip(view, 'excess') : ''"
                    :class="['gperf__main', view.slowMark === 'slow' ? 'gperf__red-cell' : '']"
                    :tabindex="view.slowMark === 'slow' ? 0 : undefined"
                  ><span v-if="view.slowMark === 'slow'" class="sr-only">{{ flagTip(view, 'excess') }}</span>{{ fmtSigned(view.extra) }}</span>
                  <span v-else class="gperf__main sq-text-3">{{ DASH }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="cache">
                  <template v-if="view.cacheRate !== null">
                    <span v-tip="flagTip(view, 'cache')" :class="['gperf__main', view.cacheLow ? 'gperf__red-cell' : '']" :tabindex="view.cacheLow ? 0 : undefined">
                      <span v-if="view.cacheLow" class="sr-only">{{ flagTip(view, 'cache') }}</span>{{ fmtPct(view.cacheRate) }}
                    </span>
                    <span class="gperf__sub">{{ t('admin.schedQuality.perf.turns', { n: fmtCount(view.row.cont.turns) }, view.row.cont.turns) }}</span>
                  </template>
                  <span v-else v-tip="cacheShortTip" class="gperf__main sq-text-3" tabindex="0">{{ t('admin.schedQuality.perf.turns', { n: fmtCount(view.row.cont.turns) }, view.row.cont.turns) }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="timeouts">
                  <template v-if="view.timeoutRate !== null">
                    <span class="gperf__main">{{ fmtPct(view.timeoutRate) }}</span>
                    <span class="gperf__sub">{{ t('admin.schedQuality.perf.times', { n: fmtCount(view.row.attempts.timeouts) }, view.row.attempts.timeouts) }}</span>
                  </template>
                  <span v-else v-tip="timeoutsShortTip" class="gperf__main sq-text-3" tabindex="0">{{ fmtCount(view.row.attempts.timeouts) }} / {{ fmtCount(view.row.attempts.n) }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="switches">
                  <span class="gperf__main">{{ fmtCount(view.switchTotal) }}</span>
                  <span v-if="view.row.switches?.profit_floor" class="gperf__sub">{{ t('admin.schedQuality.perf.profitFloor', { n: fmtCount(view.row.switches.profit_floor) }) }}</span>
                </td>
                <td class="sq-r sq-num" data-sq-cell="score">
                  <template v-if="view.row.score.value !== null">
                    <span class="gperf__main">{{ fmtScore(view.row.score.value) }}</span>
                    <span v-if="view.rank" class="gperf__sub">{{ t('admin.schedQuality.perf.rank', { place: view.rank.place, of: view.rank.of }) }}</span>
                  </template>
                  <span v-else v-tip="scoreShortTip" class="gperf__main sq-text-3" tabindex="0">{{ t('admin.schedQuality.perf.scoreShort') }}</span>
                </td>
                <td data-sq-cell="now">
                  <span class="gperf__marks">
                    <template v-if="judged(view)">
                      <span
                        v-for="dim in SCHED_DIMS"
                        :key="dim"
                        v-tip="markTip(view, dim)"
                        role="img"
                        tabindex="0"
                        :aria-label="markTip(view, dim)"
                        :class="['sq-focus', IDENTITY_SHAPES[view.row.lines[dim].identity] ?? 'sq-text-3']"
                        :data-sq-perf-line="dim"
                        :data-identity="view.row.lines[dim].identity"
                      >{{ view.row.lines[dim].identity === 'unknown' ? DASH : '' }}</span>
                    </template>
                    <template v-else>
                      <span v-for="dim in SCHED_DIMS" :key="dim" class="sq-text-3">{{ DASH }}</span>
                    </template>
                  </span>
                  <span v-if="whyOf(view)" class="gperf__sub gperf__why" data-sq-perf-why>{{ whyOf(view) }}</span>
                </td>
              </tr>
              <tr v-if="expanded.has(view.key)" class="gperf__detail-row" :data-sq-perf-detail-row="view.key">
                <td :colspan="PERF_SORT_KEYS.length + 2">
                  <div :id="`${detailIdPrefix}-${view.key}`" class="gperf__detail">
                    <SchedPerfDetail :view="view" />
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The group board's 12-hour view (已定事项 13): one row per account × model of the group with traffic in the window,
 * from GET …/groups/:id/performance (fetched once a minute while this view is open, useSchedLayerData). The figures,
 * marks, ranks and order come from performance.ts. Sorted by extra slow requests by default; each column sorts with its
 * bad end first, short rows always last. Filters: the board's model (null is every model), a priority, red only. A row
 * opens to SchedPerfDetail. Below 1024px the table scrolls sideways with the account column held on the left.
 *
 * The sort lives as long as the board stays open (another group keeps it); the priority, red only and open rows are
 * the group's and start over with another one.
 */
import { computed, reactive, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useElementSize } from '@vueuse/core'
import Icon from '@/components/icons/Icon.vue'
import SchedPerfDetail from '@/components/admin/account/quality/SchedPerfDetail.vue'
import type { BackendDim } from '@/api/admin/schedQualityTypes'
import { SCHED_BOARD_PERF_TASK, useSchedLayerData } from '@/composables/useSchedLayerData'
import { openSchedDrawer, qualityOpenerKey, useSchedLayers } from '@/composables/useSchedLayers'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { DASH, IDENTITY_SHAPES, SCHED_DIMS, schedWord } from '@/utils/schedQualityFormat'
import { vRovingTabs } from '@/utils/rovingTabs'
import { vTip } from '@/utils/tip'
import {
  DEFAULT_SORT,
  fmtCount,
  fmtPct,
  fmtTenths,
  fmtScore,
  fmtSigned,
  MIN_ATTEMPTS,
  MIN_FIRST,
  MIN_FIRST_SESSIONS,
  MIN_TURNS,
  MIN_TURN_SESSIONS,
  modelLabel,
  nextSort,
  noAction,
  noActionShort,
  PERF_SORT_KEYS,
  perfViews,
  reasonLong,
  sortViews,
  wilson,
  type PerfSort,
  type PerfSortKey,
  type PerfView
} from '@/components/admin/account/quality/performance'

const { t, te } = useI18n()
const { board } = useSchedLayers()
const { boardPerf, retry } = useSchedLayerData()
const { canView: canViewAccounts } = useAdminPermission('accounts')
const detailIdPrefix = `sq-perf-${useId()}`

const perf = computed(() => (boardPerf.id !== null && boardPerf.id === board.groupId ? boardPerf.data : null))
/** The board's model filter in words: every model, a model, or the empty one (older switches). */
const modelText = computed(() => (model.value === null ? '' : modelLabel(model.value, t)))
const failed = computed(() => boardPerf.failedId !== null && boardPerf.failedId === board.groupId)
const model = computed(() => board.model)
const group = computed(() => perf.value?.groups.find((item) => item.group_id === board.groupId))

const sort = ref<PerfSort>({ ...DEFAULT_SORT })
const priority = ref<number | null>(null)
const redOnly = ref(false)
const expanded = reactive(new Set<string>())
watch(() => board.groupId, () => {
  priority.value = null
  redOnly.value = false
  expanded.clear()
})

/** The backend counts no group with the gate off: nothing here is "no requests". */
const gateOff = computed(() => group.value?.gate_enabled === false)
/** No snapshot yet, or one still loading the window with nothing for the group yet. */
const loadingStats = computed(() => !perf.value?.at || (!perf.value.complete && views.value.length === 0))
const coveredHours = computed(() => (perf.value?.at ? Math.max(0, (Date.parse(perf.value.at) - Date.parse(perf.value.window_from)) / 3_600_000) : 0))
/** "No requests" only for a window fully covered; a partly covered one says what it covers. */
const emptyText = computed(() => {
  if (!perf.value?.complete) {
    const hours = coveredHours.value.toFixed(1)
    return model.value === null ? t('admin.schedQuality.perf.emptyPartial', { hours }) : t('admin.schedQuality.perf.emptyPartialModel', { hours, model: modelText.value })
  }
  return model.value === null ? t('admin.schedQuality.perf.empty') : t('admin.schedQuality.perf.emptyModel', { model: modelText.value })
})

const views = computed(() => {
  const data = perf.value
  if (!data) return []
  const rows = data.rows.filter((row) => row.group_id === board.groupId)
  return perfViews(rows, data.groups, { byModel: model.value !== null })
})
const ofModel = computed(() => (model.value === null ? views.value : views.value.filter((view) => view.row.model === model.value)))
const priorities = computed(() => [...new Set(ofModel.value.map((view) => view.row.priority))].sort((a, b) => a - b))
// A priority no longer present (another model picked) reads as all.
watch(priorities, (list) => {
  if (priority.value !== null && !list.includes(priority.value)) priority.value = null
})
const ofPriority = computed(() => (priority.value === null ? ofModel.value : ofModel.value.filter((view) => view.row.priority === priority.value)))
const redCount = computed(() => ofPriority.value.filter((view) => view.red).length)
const shown = computed(() => sortViews(redOnly.value && redCount.value > 0 ? ofPriority.value.filter((view) => view.red) : ofPriority.value, sort.value))

function pickPriority(value: number | null) {
  priority.value = value
}
function toggle(key: string) {
  if (expanded.has(key)) expanded.delete(key)
  else expanded.add(key)
}

// The detail stays in view while the table scrolls sideways: as wide as the visible part, held on the left.
const scrollRef = ref<HTMLElement | null>(null)
const { width: visible } = useElementSize(scrollRef)

const windowText = computed(() => {
  const data = perf.value!
  if (!data.at) return t('admin.schedQuality.perf.windowLoading')
  const total = ofModel.value.reduce((sum, view) => sum + view.row.requests, 0)
  const requests = t('admin.schedQuality.perf.total', { n: fmtCount(total) }, total)
  const parts = [
    t('admin.schedQuality.perf.window', { hours: data.window_hours, from: formatShortTime(data.window_from), to: formatShortTime(data.at) })
  ]
  if (!data.complete) {
    parts.push(coveredHours.value < 0.05
      ? t('admin.schedQuality.perf.windowLoading')
      : t('admin.schedQuality.perf.windowPartial', { hours: coveredHours.value.toFixed(1), from: formatShortTime(data.window_from) }))
  }
  parts.push(t('admin.schedQuality.perf.updated', { time: formatShortTime(data.at) }))
  if (!loadingStats.value && !gateOff.value) {
    parts.push(model.value === null ? requests : t('admin.schedQuality.perf.totalModel', { model: modelText.value, requests }))
  }
  return parts.join(' · ')
})

function ariaSort(col: PerfSortKey) {
  if (sort.value.key !== col) return undefined
  return sort.value.dir === 'asc' ? 'ascending' : 'descending'
}
const arrow = (col: PerfSortKey) => (sort.value.key !== col ? '' : sort.value.dir === 'asc' ? '▲' : '▼')

function colTip(col: PerfSortKey): string {
  const g = group.value
  switch (col) {
    case 'requests':
      return t(model.value === null ? 'admin.schedQuality.perf.tip.requests' : 'admin.schedQuality.perf.tip.requestsModel')
    case 'slow':
      return g ? t('admin.schedQuality.perf.tip.slow', { s: g.ttft_seconds }) : t('admin.schedQuality.perf.tip.slowAny')
    case 'cache':
      return g ? t('admin.schedQuality.perf.tip.cache', { min: fmtPct(g.min_cache_ratio, 0) }) : t('admin.schedQuality.perf.tip.cacheAny')
    case 'score':
      return t('admin.schedQuality.perf.tip.score', { use: t('admin.schedQuality.perf.scoreUse') })
    default:
      return t(`admin.schedQuality.perf.tip.${col}`)
  }
}

const subOf = (view: PerfView) => model.value === null
  ? t('admin.schedQuality.perf.subModel', { priority: view.row.priority, model: modelLabel(view.row.model, t) })
  : t('admin.schedQuality.perf.sub', { priority: view.row.priority })

const shortTip = computed(() => t('admin.schedQuality.perf.shortTip', { min: MIN_FIRST, sessions: MIN_FIRST_SESSIONS }))
const cacheShortTip = computed(() => t('admin.schedQuality.perf.cacheShortTip', { min: MIN_TURNS, sessions: MIN_TURN_SESSIONS }))
const timeoutsShortTip = computed(() => t('admin.schedQuality.perf.timeoutsShortTip', { min: MIN_ATTEMPTS }))
const scoreShortTip = computed(() => t('admin.schedQuality.perf.scoreShortTip', { min: MIN_FIRST, sessions: MIN_FIRST_SESSIONS }))

const expectedText = (view: PerfView) => view.expectedRate === null
  ? t('admin.schedQuality.perf.expectedNone')
  : t('admin.schedQuality.perf.expected', { rate: fmtPct(view.expectedRate) })
function expectedTip(view: PerfView): string {
  if (view.expectedRate === null) return t('admin.schedQuality.perf.expectedNoneTip')
  if (!view.expectedEnough) return t('admin.schedQuality.perf.expectedFewTip', { n: fmtCount(view.row.first.expected_n), min: MIN_FIRST })
  return ''
}
/** Why the excess is not given: too few first sends, else nothing (or too little) to set them against. */
const missingTip = (view: PerfView) => (!view.firstEnough ? shortTip.value : expectedTip(view))

/** The excess cell: red when slow for sure, green when fast for sure. */
const markClass = (view: PerfView) => (view.slowMark === 'slow' ? 'gperf__red-cell' : view.slowMark === 'fast' ? 'sq-pass-word' : '')
function flagTip(view: PerfView, cell: 'excess' | 'cache'): string {
  if (cell === 'cache') {
    return view.cacheLow && view.group
      ? t('admin.schedQuality.perf.flag.cacheLow', { rate: fmtPct(view.cacheRate!), min: fmtPct(view.group.min_cache_ratio, 0) })
      : ''
  }
  if (!view.slowMark) return ''
  const bounds = wilson(view.row.first.slow, view.row.first.n)
  const expected = fmtPct(view.expectedRate!)
  return view.slowMark === 'slow'
    ? t('admin.schedQuality.perf.flag.slow', { lower: fmtPct(bounds.lower), expected })
    : t('admin.schedQuality.perf.flag.fast', { upper: fmtPct(bounds.upper), expected })
}

/** The lines are judged here: a member of a group with the gate on. */
const judged = (view: PerfView) => view.row.member && view.group?.gate_enabled !== false
function markTip(view: PerfView, dim: BackendDim): string {
  const line = view.row.lines[dim]
  const parts = [t(`admin.schedQuality.dim.${dim}`), schedWord('identity', line.identity, t, te)]
  const latest = line.last_eval
  if (latest) parts.push(reasonLong(latest, t, te, { trialEnd: line.identity === 'trial' ? line.until : null, formatTime: formatShortTime }))
  if (line.until && line.identity === 'rear') parts.push(t('admin.schedQuality.perf.rearUntil', { time: formatShortTime(line.until) }))
  else if (line.until && line.identity === 'trial' && latest?.reason !== 'trial_open') parts.push(t('admin.schedQuality.perf.trialUntil', { time: formatShortTime(line.until) }))
  return parts.join(' · ')
}
function whyOf(view: PerfView): string {
  const why = noAction(view)
  return why ? noActionShort(why.eval, t, te) : ''
}
</script>

<style scoped>
@reference "../../../style.css";

.gperf__line {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-bottom: 8px;
  font-size: 13px;
}
.gperf__filters {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
  margin-bottom: 12px;
  font-size: 13px;
}
.gperf__prio-label {
  flex-shrink: 0;
  white-space: nowrap;
}
.gperf__prio {
  display: flex;
  align-items: center;
  gap: 8px;
}
.gperf__tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.gperf__tab,
.gperf__red {
  height: 26px;
  padding: 0 8px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--sq-text);
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
}
.gperf__red {
  box-shadow: inset 0 0 0 1px var(--sq-line);
  color: var(--sq-cool-fg);
}
.gperf__tab[aria-selected='true'],
.gperf__red[aria-pressed='true'] {
  background: theme('colors.primary.100');
  color: theme('colors.primary.700');
}
.gperf__tab:focus-visible,
.gperf__red:focus-visible,
.gperf__sort:focus-visible {
  outline: 2px solid var(--sq-ring);
  outline-offset: 1px;
}
/* Ten columns: 8px each side (the order view's 12px would not fit the 1024px dialog in English). */
.gperf__table th,
.gperf__table td {
  padding-right: 8px;
  padding-left: 8px;
}
.gperf__table td {
  vertical-align: top;
}
.gperf__sort {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: inherit;
  font: inherit;
}
.gperf__arrow {
  display: inline-block;
  width: 0.8em;
  font-size: 9px;
}
.gperf__main,
.gperf__sub {
  display: block;
}
.gperf__sub {
  margin-top: 2px;
  color: var(--sq-text-3);
  font-size: 12px;
}
.gperf__red-cell {
  color: var(--sq-cool-fg);
  font-weight: 600;
}
/* The account column stays on the left while the table scrolls sideways, on the dialog's own background. */
.gperf__acct {
  position: sticky;
  left: 0;
  z-index: 1;
  background: theme('colors.white');
}
.gperf__who {
  display: flex;
  align-items: flex-start;
  gap: 4px;
}
/* A long account or model name is cut (its title has it whole), so the held column stays narrow; never narrower than
   120px, or a table wider than the dialog would squeeze it to nothing. */
.gperf__id {
  display: grid;
  min-width: 120px;
  max-width: 200px;
}
.gperf__name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gperf__model {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gperf__toggle {
  display: grid;
  width: 20px;
  height: 20px;
  flex: none;
  place-items: center;
  margin-left: -4px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--sq-text-3);
}
.gperf__toggle svg {
  transition: transform var(--motion-fast) ease-out;
}
.gperf__toggle[aria-expanded='true'] svg {
  transform: rotate(90deg);
}
.gperf__toggle:focus-visible {
  outline: 2px solid var(--sq-ring);
  outline-offset: -2px;
}
@media (prefers-reduced-motion: reduce) {
  .gperf__toggle svg {
    transition: none;
  }
}
.gperf__marks {
  display: inline-flex;
  align-items: center;
  gap: 10px;
  height: 20px;
}
/* Why not acted: under the marks, wrapping in a narrow column instead of widening the table. On a phone the table
   scrolls sideways anyway: there it never gets squeezed to a few characters a line. From 640px the table must fit the
   dialog (1024 in English included), so no floor there. */
.gperf__why {
  max-width: 11em;
  white-space: normal;
}
@media (max-width: 639px) {
  .gperf__why {
    min-width: 8em;
  }
}
.gperf__detail-row td {
  padding-top: 0;
  border-top: 0;
}
/* The opened detail stays in the visible part of a table that scrolls sideways. */
.gperf__detail {
  position: sticky;
  left: 8px;
  max-width: calc(var(--gperf-visible, 100vw) - 16px);
  padding: 8px 0 4px 20px;
}
</style>

<!-- Dark theme: not scoped (a scoped `:global(.dark) .x` compiles to a bare `.dark` rule on <html>); every class here
     carries the gperf__ prefix. The held account column takes the dialog's own dark background. -->
<style>
@reference "../../../style.css";

.dark .gperf__acct {
  background: theme('colors.dark.800');
}
.dark .gperf__tab[aria-selected='true'],
.dark .gperf__red[aria-pressed='true'] {
  background: rgb(19 78 74 / 0.3);
  color: theme('colors.primary.300');
}
</style>
