<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqRecords')" width="wide" @close="$emit('close')">
    <!-- Only the round list scrolls: the dialog body becomes a flex column (style below) and this column shrinks into it. -->
    <div ref="root" class="iq-records flex min-h-0 flex-1 flex-col">
      <div class="mb-3 flex shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-2 sm:mb-4">
        <p class="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <span class="break-words text-sm font-medium text-gray-900 dark:text-gray-100">{{ account?.name }}</span>
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqHistoryRetention') }}</span>
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="btn btn-secondary inline-flex items-center gap-2" data-testid="iq-run-all" :disabled="running !== null || !targets.length || targets.every(iqTargetBusy)" @click="run()">
            <Icon name="play" size="sm" />{{ t('admin.accounts.iqRunAll') }}
          </button>
          <button type="button" class="btn btn-secondary inline-flex items-center gap-2" :disabled="downloading || !account" @click="downloadDiagnostics">
            <Icon name="download" size="sm" />{{ t('admin.accounts.iqDownload') }}
          </button>
          <button type="button" class="btn btn-secondary h-10 w-10 p-0" data-testid="iq-refresh" :disabled="loading || !account" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="reload">
            <Icon name="refresh" size="sm" :class="loading && 'animate-spin'" />
          </button>
        </div>
      </div>

      <div class="shrink-0">
        <p v-if="runMessage" role="status" class="mb-3 text-sm text-gray-700 dark:text-gray-300">{{ runMessage }}</p>
        <p v-for="message in [runError, targetsError, downloadError].filter(Boolean)" :key="message" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ message }}</p>
      </div>

      <div v-if="targets.length > 1" role="tablist" :aria-label="t('admin.accounts.iqTargets')" class="tabs mb-3 shrink-0 overflow-x-auto sm:mb-4">
        <button v-for="target in targets" :key="target.model" type="button" role="tab" :aria-selected="target.model === selected" :data-testid="'iq-tab-' + target.model" class="tab inline-flex shrink-0 items-center gap-2 px-3 py-1.5" :class="target.model === selected && 'tab-active'" @click="selectModel(target.model)">
          <Icon v-if="iqTargetBusy(target)" name="refresh" size="xs" class="animate-spin text-primary-500" />
          <span v-else class="h-2 w-2 rounded-full" :class="dotClass(target)" aria-hidden="true" />
          <span class="max-w-[12rem] truncate">{{ iqModelLabel(target.model) }}</span>
        </button>
      </div>

      <section v-if="current" ref="summary" class="mb-3 shrink-0 border-b border-gray-200 pb-3 dark:border-dark-600 sm:pb-4" data-testid="iq-target-summary" :aria-label="current.model">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0 space-y-1">
            <p class="flex flex-wrap items-center gap-2">
              <span class="badge" :class="verdictBadge(current)">{{ verdictLabel(current) }}</span>
              <span class="break-all text-sm text-gray-500 dark:text-dark-400">{{ current.model }}</span>
            </p>
            <p class="text-sm text-gray-800 dark:text-gray-200" data-testid="iq-verdict-explain">{{ verdictExplain(current) }}</p>
            <p v-if="current.gate_active && current.gate_model && current.gate_model !== current.model" class="break-all text-xs text-gray-500 dark:text-dark-400" data-testid="iq-gate-model">{{ t('admin.accounts.iqGateModel', { model: current.gate_model }) }}</p>
            <p v-if="current.state === 'paused'" class="text-sm text-amber-700 dark:text-amber-400" data-testid="iq-paused">{{ pausedLine(current) }}</p>
          </div>
          <button type="button" class="btn btn-primary inline-flex shrink-0 items-center gap-2" data-testid="iq-run-target" :disabled="running !== null || iqTargetBusy(current)" @click="run(current.model)">
            <Icon :name="iqTargetBusy(current) || running === current.model ? 'refresh' : 'play'" size="sm" :class="(iqTargetBusy(current) || running === current.model) && 'animate-spin'" />
            {{ t(current.state === 'queued' ? 'admin.accounts.iqStates.queued' : current.state === 'running' ? 'admin.accounts.iqStates.running' : 'admin.accounts.iqRunTarget') }}
          </button>
        </div>
        <dl class="mt-3 grid grid-cols-2 gap-x-5 gap-y-2 text-xs sm:mt-4 sm:grid-cols-4">
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqLastAssessment') }}</dt><dd class="mt-1 tabular-nums text-gray-900 dark:text-gray-100">{{ current.last_run_at ? formatDateTime(current.last_run_at) : t('admin.accounts.iqNotChecked') }}</dd></div>
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNextCheck') }}</dt><dd class="mt-1 tabular-nums text-gray-900 dark:text-gray-100" data-testid="iq-next">{{ nextLabel(current) }}</dd></div>
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqEffort') }}</dt><dd class="mt-1 text-gray-900 dark:text-gray-100">{{ iqEffortLabel(current.reasoning_effort) }}</dd></div>
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqInterval') }}</dt><dd class="mt-1 tabular-nums text-gray-900 dark:text-gray-100">{{ current.interval_minutes }}</dd></div>
        </dl>
      </section>

      <div v-if="selected" role="group" :aria-label="t('admin.accounts.iqFilterLabel')" class="mb-2 inline-flex shrink-0 self-start rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-dark-600 dark:bg-dark-800">
        <button v-for="option in FILTERS" :key="option.value" type="button" class="rounded-md px-3 py-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500" :class="filter === option.value ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white' : 'text-gray-500 hover:text-gray-800 dark:text-dark-300 dark:hover:text-white'" :aria-pressed="filter === option.value" :data-testid="'iq-filter-' + (option.value || 'all')" @click="setFilter(option.value)">{{ t(option.label) }}</button>
      </div>

      <div ref="list" class="min-h-[8rem] flex-1 overflow-y-auto overscroll-contain" data-testid="iq-round-list" :aria-busy="loading">
        <div v-if="roundsError" role="alert" class="py-4 text-center">
          <p class="text-sm text-red-600 dark:text-red-400">{{ roundsError }}</p>
          <button type="button" class="btn btn-secondary mt-3" @click="reloadPage">{{ t('common.refresh') }}</button>
        </div>
        <div v-if="loading && !rounds.length" role="status" class="flex items-center justify-center gap-2 py-12 text-sm text-gray-500 dark:text-dark-400"><Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}</div>
        <div v-else-if="!rounds.length && !roundsError" class="py-10 text-center text-gray-500 dark:text-dark-400"><Icon name="clock" class="mx-auto mb-3" /><p class="text-sm">{{ t(filter ? 'admin.accounts.iqEmptyAbnormal' : 'admin.accounts.iqEmpty') }}</p></div>

        <ol v-if="rounds.length" class="divide-y divide-gray-200 transition-opacity dark:divide-dark-600" :class="loading && 'opacity-60'">
          <li v-for="round in rounds" :key="round.id" data-testid="iq-round">
            <button type="button" class="flex w-full flex-wrap items-center gap-x-3 gap-y-1 rounded-lg px-2 py-3 text-left hover:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500 dark:hover:bg-dark-700/50" :aria-expanded="expanded === round.id" @click="toggle(round.id)">
              <Icon name="chevronRight" size="sm" class="shrink-0 text-gray-400 transition-transform" :class="expanded === round.id && 'rotate-90'" />
              <span class="badge" :class="roundBadge(round)">{{ round.finished_at ? t('admin.accounts.iqCheckStatus.' + (round.status || 'unknown')) : t('admin.accounts.iqStates.running') }}</span>
              <span v-if="round.mode === 'recheck'" class="badge badge-gray">{{ t('admin.accounts.iqRecheck') }}</span>
              <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.accounts.iqAttemptCount', { count: round.attempts.length }) }}</span>
              <span v-if="round.reason && round.reason !== 'correct_answer'" class="min-w-0 truncate text-sm text-gray-500 dark:text-dark-400">{{ reasonLabel(round.reason) }}</span>
              <time :datetime="round.started_at" class="ml-auto text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ formatDateTime(round.started_at) }}</time>
            </button>
            <Transition :css="false" @enter="revealAttempts">
              <ol v-if="expanded === round.id" class="space-y-3 pb-4 pl-2 sm:pl-8" data-testid="iq-attempts">
                <li v-if="round.outbound_model && round.outbound_model !== round.model" class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqOutboundModel', { model: round.outbound_model }) }}</li>
                <li v-for="attempt in round.attempts" :key="attempt.attempt_no" class="iq-attempt rounded-xl bg-gray-50 p-4 dark:bg-dark-700/50" data-testid="iq-attempt">
                  <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <span class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.accounts.iqAttemptNumber', { number: attempt.attempt_no }) }}</span>
                    <span class="inline-flex items-center gap-1 text-sm font-medium" :class="outcomeClass(attempt)" data-testid="iq-outcome">
                      <Icon :name="outcomeIcon(attempt)" size="sm" />{{ outcomeLabel(attempt) }}
                    </span>
                    <span class="ml-auto text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ [iqEffortLabel(attempt.effort), attempt.latency_ms != null ? iqSeconds(attempt.latency_ms) : ''].filter(Boolean).join(' · ') }}</span>
                  </div>
                  <div class="mt-3 grid grid-cols-1 gap-4" :class="attempt.challenge && 'sm:grid-cols-[auto_minmax(0,1fr)] sm:gap-6'">
                    <table v-if="attempt.challenge" class="self-start text-sm tabular-nums" data-testid="iq-challenge">
                      <caption class="sr-only">{{ t('admin.accounts.iqChallengeCaption') }}</caption>
                      <thead>
                        <tr :title="t('admin.accounts.iqChallengeCaption')" class="cursor-help">
                          <th class="px-2 py-1"><span class="sr-only">{{ t('admin.accounts.iqShape') }}</span></th>
                          <th v-for="flavor in iqChallengeTable(attempt.challenge).flavors" :key="flavor" scope="col" class="px-3 py-1 text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqFlavors.' + IQ_FLAVORS[flavor]) }}</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr v-for="row in iqChallengeTable(attempt.challenge).rows" :key="row.shape" class="border-t border-gray-200 dark:border-dark-600">
                          <th scope="row" class="px-2 py-1.5 text-left text-xs font-medium text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqShapes.' + IQ_SHAPES[row.shape]) }}</th>
                          <td v-for="(count, index) in row.counts" :key="index" class="px-3 py-1.5 text-center text-gray-900 dark:text-gray-100">{{ count }}</td>
                        </tr>
                      </tbody>
                    </table>
                    <dl class="grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-4 gap-y-2 self-start text-sm" data-testid="iq-answers">
                      <template v-if="attempt.expected">
                        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqExpected') }}</dt>
                        <dd class="tabular-nums text-gray-900 dark:text-gray-100" data-testid="iq-expected">
                          <span class="font-semibold">{{ attempt.expected }}</span>
                          <span v-if="attempt.challenge" class="ml-1.5 text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqDerivation', { circle: attempt.challenge.draw[0], star: attempt.challenge.draw[1] }) }}</span>
                        </dd>
                      </template>
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNormalizedAnswer') }}</dt>
                      <dd class="break-words tabular-nums" data-testid="iq-answer">
                        <template v-if="attempt.answer">
                          <span class="font-semibold" :class="outcomeClass(attempt)">{{ attempt.answer }}</span>
                          <span v-if="isNaive(attempt)" class="ml-1 text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNaiveMatch') }}</span>
                        </template>
                        <span v-else class="text-gray-600 dark:text-dark-300">{{ attempt.reason ? reasonLabel(attempt.reason) : '—' }}</span>
                      </dd>
                      <template v-if="attempt.challenge">
                        <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNaive') }}</dt>
                        <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ attempt.challenge.naive }}</dd>
                      </template>
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqReportedModel') }}</dt>
                      <dd class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1" data-testid="iq-reported-model">
                        <template v-if="declarations(attempt).length">
                          <span v-for="item in declarations(attempt)" :key="item.source" class="break-all text-gray-900 dark:text-gray-100"><span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqDeclarationSources.' + item.source) }}</span> {{ item.values.join(', ') }}</span>
                        </template>
                        <span v-else class="break-all text-gray-900 dark:text-gray-100">{{ attempt.reported_model || '—' }}</span>
                        <span v-if="attempt.model_class" class="badge" :class="classBadge(attempt)" data-testid="iq-model-class">{{ classLabel(attempt.model_class) }}</span>
                        <span v-if="attempt.diagnostic?.model_identity?.flags?.includes('source_disagreement')" class="badge badge-warning">{{ t('admin.accounts.iqSourceDisagreement') }}</span>
                      </dd>
                    </dl>
                  </div>
                  <details v-if="technicalRows(attempt).length" class="mt-3" data-testid="iq-technical-details">
                    <summary class="cursor-pointer text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqTechnicalDetails') }}</summary>
                    <dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-4 gap-y-1.5 text-xs">
                      <template v-for="row in technicalRows(attempt)" :key="row.key">
                        <dt class="text-gray-500 dark:text-dark-400">{{ row.label }}</dt>
                        <dd class="break-words tabular-nums text-gray-900 dark:text-gray-100" :class="row.key === 'request_id' && 'select-all break-all font-mono'" :data-testid="'iq-tech-' + row.key">{{ row.value }}</dd>
                      </template>
                    </dl>
                  </details>
                </li>
              </ol>
            </Transition>
          </li>
        </ol>
      </div>
      <Pagination v-if="total > 0" class="-mx-4 -mb-3 shrink-0 rounded-b-2xl sm:-mx-6 sm:-mb-4" :total="total" :page="page" :page-size="PAGE_SIZE" :show-page-size-selector="false" @update:page="goToPage" />
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import { downloadIQCheckDiagnostics } from '@/api/admin/accounts'
import { adminAPI } from '@/api/admin'
import type { Account, IQAttempt, IQRound, IQRoundFilter, IQTarget } from '@/types'
import { formatDateTime } from '@/utils/format'
import { isReducedMotion } from '@/composables/useMotionPreference'
import { IQ_FLAVORS, IQ_SHAPES, iqAttemptOutcome, iqChallengeTable, IQ_CLOCK, iqEffortLabel, iqModelClassTone, iqModelLabel, iqSeconds, iqTargetBusy, iqTechnicalRows, sortIQTargets } from './iqCheckPresentation'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [targets: IQTarget[]] }>()
const { t, te } = useI18n()
const POLL_MS = 3000
const PAGE_SIZE = 10
const FILTERS: { value: IQRoundFilter; label: string }[] = [{ value: '', label: 'admin.accounts.iqFilterAll' }, { value: 'abnormal', label: 'admin.accounts.iqFilterAbnormal' }]

const reasonLabel = (reason: string) => te(`admin.accounts.iqReasons.${reason}`) ? t(`admin.accounts.iqReasons.${reason}`) : reason
const classLabel = (value: string) => te(`admin.accounts.iqModelClasses.${value}`) ? t(`admin.accounts.iqModelClasses.${value}`) : value

// ---- target summary: can this model be used now, why, and when is the recheck ----
const verdictBadge = (target: IQTarget) => target.gate_active || target.verdict === 'degraded' ? 'badge-danger' : target.verdict === 'smart' && target.enabled ? 'badge-success' : 'badge-gray'
const dotClass = (target: IQTarget) => target.gate_active || target.verdict === 'degraded' ? 'bg-red-500' : target.verdict === 'smart' && target.enabled ? 'bg-emerald-500' : 'bg-gray-300 dark:bg-dark-500'
const verdictLabel = (target: IQTarget) => target.gate_active ? t('admin.accounts.iqGateActive') : target.enabled ? t('admin.accounts.iqCheckStatus.' + target.verdict) : t('admin.accounts.iqOff')
function verdictExplain(target: IQTarget) {
  if (target.gate_active) return t('admin.accounts.iqExplain.gate') + (target.gate_reset_at ? ' ' + t('admin.accounts.iqRecheckAt', { time: formatDateTime(target.gate_reset_at, IQ_CLOCK) }) : '')
  if (!target.enabled) return t('admin.accounts.iqExplain.off')
  return t('admin.accounts.iqExplain.' + target.verdict)
}
// An unschedulable account resumes by itself, so its reason is a full sentence rather than "run a manual check to resume".
const pausedLine = (target: IQTarget) => target.paused_reason === 'account_unschedulable' ? reasonLabel(target.paused_reason) : t('admin.accounts.iqPausedBecause', { reason: reasonLabel(target.paused_reason) })
function nextLabel(target: IQTarget) {
  if (target.state === 'running') return t('admin.accounts.iqStates.running')
  if (target.state === 'paused') return '—'
  // Due but not yet claimed: the schedule time has passed, so show the wait rather than a past time.
  if (target.state === 'queued') return target.next_run_at && new Date(target.next_run_at).getTime() <= Date.now() ? t('admin.accounts.iqAwaitingRun') : t('admin.accounts.iqStates.queued')
  if (!target.enabled) return '—'
  const times = [target.next_run_at, target.not_before].filter((value): value is string => !!value)
  return times.length ? formatDateTime(times.reduce((a, b) => (new Date(a) > new Date(b) ? a : b))) : '—'
}

// ---- per-round and per-attempt detail ----
const roundBadge = (round: IQRound) => !round.finished_at ? 'badge-primary' : round.status === 'smart' ? 'badge-success' : round.status === 'degraded' ? 'badge-danger' : 'badge-gray'
const outcomeLabel = (attempt: IQAttempt) => t('admin.accounts.iqOutcomes.' + iqAttemptOutcome(attempt))
const outcomeIcon = (attempt: IQAttempt) => ({ correct: 'checkCircle', wrong: 'xCircle', naive: 'xCircle', downgrade: 'xCircle', conflict: 'exclamationTriangle', unknown: 'questionCircle' } as const)[iqAttemptOutcome(attempt)]
const outcomeClass = (attempt: IQAttempt) => ({ correct: 'text-emerald-700 dark:text-emerald-400', wrong: 'text-red-700 dark:text-red-400', naive: 'text-red-700 dark:text-red-400', downgrade: 'text-red-700 dark:text-red-400', conflict: 'text-amber-700 dark:text-amber-400', unknown: 'text-gray-600 dark:text-dark-300' })[iqAttemptOutcome(attempt)]
const isNaive = (attempt: IQAttempt) => !!attempt.challenge && attempt.answer === String(attempt.challenge.naive) && attempt.answer !== attempt.expected
const classBadge = (attempt: IQAttempt) => ({ danger: 'badge-danger', warning: 'badge-warning', neutral: 'badge-gray' })[iqModelClassTone(attempt)]
const technicalRows = (attempt: IQAttempt) => iqTechnicalRows(attempt, t, te, formatDateTime)
/** Body and header declarations side by side when both sources reported a model. */
function declarations(attempt: IQAttempt) {
  const identity = attempt.diagnostic?.model_identity
  if (!identity?.body?.length || !identity.header?.length) return []
  return [{ source: 'body', values: identity.body }, { source: 'header', values: identity.header }]
}

// ---- data loading and polling ----
// Pages are live, not snapshots: polling replaces page 1 only; older pages stay frozen until the user pages again.
const targets = ref<IQTarget[]>([])
const selected = ref('')
const current = computed(() => targets.value.find(target => target.model === selected.value))
const rounds = ref<IQRound[]>([])
const total = ref(0)
const page = ref(1)
const filter = ref<IQRoundFilter>('')
const expanded = ref<number | null>(null)
const list = ref<HTMLElement>()
const loading = ref(false)
const targetsError = ref('')
const roundsError = ref('')
let session = 0
let roundsVersion = 0
let controller = new AbortController()
let pollTimer: ReturnType<typeof setTimeout> | undefined

const pickDefault = (list: IQTarget[]) => (list.find(target => target.gate_active) ?? list.find(target => target.enabled) ?? list[0])?.model ?? ''

async function loadTargets(s: number) {
  const id = props.account?.id
  if (!id) return
  try {
    const list = sortIQTargets(await adminAPI.accounts.getIQCheck(id, controller.signal))
    if (s !== session) return
    targets.value = list
    targetsError.value = ''
    if (!list.some(target => target.model === selected.value)) selected.value = pickDefault(list)
    emit('updated', list)
  } catch { if (s === session) targetsError.value = t('admin.accounts.iqStatusFailed') }
}

/** Loads the current page; any newer request (page, filter, model, account or poll) discards this response. */
async function loadRounds(): Promise<void> {
  const id = props.account?.id
  const model = selected.value
  if (!id || !model) return
  const s = session
  const version = ++roundsVersion
  try {
    const result = await adminAPI.accounts.listIQResults(id, { model, page: page.value, page_size: PAGE_SIZE, filter: filter.value }, controller.signal)
    if (s !== session || version !== roundsVersion) return
    // The page emptied under us (records expired): fall back to the last page that exists, page 1 when none are left.
    if (!result.rounds.length && page.value > 1) {
      page.value = Math.max(1, Math.ceil(result.total / PAGE_SIZE))
      return loadRounds()
    }
    roundsError.value = ''
    rounds.value = result.rounds
    total.value = result.total
    if (expanded.value !== null && !result.rounds.some(round => round.id === expanded.value)) expanded.value = null
  } catch { if (s === session && version === roundsVersion) roundsError.value = t('admin.accounts.iqHistoryFailed') }
  finally { if (s === session && version === roundsVersion) loading.value = false }
}

function stopPolling() { clearTimeout(pollTimer); pollTimer = undefined }
function schedulePoll() {
  stopPolling()
  if (!props.show || !targets.value.some(iqTargetBusy)) return
  pollTimer = setTimeout(() => {
    if (document.visibilityState === 'hidden') schedulePoll()
    else void refresh(true)
  }, POLL_MS)
}

async function refresh(poll: boolean) {
  stopPolling()
  const s = session
  if (!props.show || !props.account) return
  if (!poll) loading.value = true
  await loadTargets(s)
  if (s !== session) return
  if (page.value === 1) await loadRounds()
  if (s !== session) return
  loading.value = false
  schedulePoll()
}

/** Shows another page, filter or model: collapse, scroll the list to the top and load. */
function showPage(next: number) {
  page.value = next
  expanded.value = null
  if (list.value) list.value.scrollTop = 0
  loading.value = true
  void loadRounds()
}
const goToPage = (next: number) => showPage(next)
const reloadPage = () => showPage(page.value)
function reload() { page.value = 1; void refresh(false) }

function setFilter(value: IQRoundFilter) {
  if (value === filter.value) return
  filter.value = value
  showPage(1)
}

function selectModel(model: string) {
  if (model === selected.value) return
  selected.value = model
  rounds.value = []
  total.value = 0
  roundsError.value = ''
  showPage(1)
}

function toggle(id: number) {
  expanded.value = expanded.value === id ? null : id
}

// ---- manual checks ----
const running = ref<string | null>(null)
const runMessage = ref('')
const runError = ref('')
async function run(model?: string) {
  const id = props.account?.id
  if (!id || running.value !== null) return
  const s = session
  running.value = model ?? ''
  runMessage.value = ''; runError.value = ''
  try {
    const results = await adminAPI.accounts.runIQCheck(id, model ? [model] : undefined)
    if (s !== session) return
    const accepted = results.filter(result => result.state !== 'rejected').map(result => iqModelLabel(result.model))
    const rejected = results.filter(result => result.state === 'rejected').map(result => `${iqModelLabel(result.model)}: ${reasonLabel(result.reason || '')}`)
    if (accepted.length) runMessage.value = t('admin.accounts.iqRunAccepted', { models: accepted.join(', ') })
    if (rejected.length) runError.value = t('admin.accounts.iqRunRejected', { details: rejected.join('; ') })
    // The new round lands on page 1.
    if (accepted.length && page.value !== 1) { page.value = 1; expanded.value = null; if (list.value) list.value.scrollTop = 0 }
    await refresh(true)
  } catch { if (s === session) runError.value = t('admin.accounts.iqFailed') }
  finally { if (s === session) running.value = null }
}

// ---- diagnostics download ----
const downloading = ref(false)
const downloadError = ref('')
async function downloadDiagnostics() {
  const id = props.account?.id
  if (!id || downloading.value) return
  const s = session
  downloading.value = true
  downloadError.value = ''
  try {
    const blob = await downloadIQCheckDiagnostics(id)
    if (s !== session) return
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url; link.download = 'iq-check-diagnostics.json'
    document.body.appendChild(link); link.click(); link.remove()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch { if (s === session) downloadError.value = t('admin.accounts.iqDownloadFailed') }
  finally { if (s === session) downloading.value = false }
}

// ---- motion: attempts settle in when a round opens; the summary eases in on target switch ----
const root = ref<HTMLElement>()
const summary = ref<HTMLElement>()
let motion: gsap.Context | undefined
onMounted(() => { motion = gsap.context(() => {}, root.value) })
function revealAttempts(el: Element, done: () => void) {
  if (!motion || isReducedMotion()) return done()
  motion.add(() => gsap.from(el.querySelectorAll('.iq-attempt'), { autoAlpha: 0, y: -6, duration: 0.22, ease: 'expo.out', stagger: { amount: 0.09 }, clearProps: 'opacity,visibility,transform', onComplete: done }))
}
watch(selected, async (_value, previous) => {
  if (!previous || !motion || isReducedMotion()) return
  await nextTick()
  if (summary.value) motion.add(() => gsap.from(summary.value!, { autoAlpha: 0.4, y: 4, duration: 0.2, ease: 'expo.out', clearProps: 'opacity,visibility,transform' }))
})

function reset() {
  session++
  controller.abort()
  controller = new AbortController()
  stopPolling()
  targets.value = []; selected.value = ''; rounds.value = []; total.value = 0; page.value = 1; filter.value = ''; expanded.value = null
  targetsError.value = ''; roundsError.value = ''; downloadError.value = ''; runMessage.value = ''; runError.value = ''
  loading.value = false; downloading.value = false; running.value = null
}
watch([() => props.show, () => props.account?.id], () => { reset(); void refresh(false) }, { immediate: true })
onBeforeUnmount(() => { reset(); motion?.revert() })
</script>

<style>
/* Scoped to this dialog: lets the body hand its height to the record column so only the round list scrolls. */
.modal-body:has(> .iq-records) {
  display: flex;
  flex-direction: column;
}
</style>
