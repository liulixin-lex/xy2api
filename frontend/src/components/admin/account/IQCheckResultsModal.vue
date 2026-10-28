<template>
  <BaseDialog :show="show" :title="t('admin.accounts.iqRecords')" width="wide" @close="$emit('close')">
    <div ref="root">
      <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="break-words text-sm font-medium text-gray-900 dark:text-gray-100">{{ account?.name }}</p>
          <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqHistoryRetention') }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="btn btn-secondary inline-flex items-center gap-2" data-testid="iq-run-all" :disabled="running !== null || !targets.length || targets.every(iqTargetBusy)" @click="run()">
            <Icon name="play" size="sm" />{{ t('admin.accounts.iqRunAll') }}
          </button>
          <button type="button" class="btn btn-secondary inline-flex items-center gap-2" :disabled="downloading || !account" @click="downloadDiagnostics">
            <Icon name="download" size="sm" />{{ t('admin.accounts.iqDownload') }}
          </button>
          <button type="button" class="btn btn-secondary h-10 w-10 p-0" :disabled="loading || !account" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="refresh(false)">
            <Icon name="refresh" size="sm" :class="loading && 'animate-spin'" />
          </button>
        </div>
      </div>

      <p v-if="runMessage" role="status" class="mb-3 text-sm text-gray-700 dark:text-gray-300">{{ runMessage }}</p>
      <p v-for="message in [runError, targetsError, downloadError].filter(Boolean)" :key="message" role="alert" class="mb-3 text-sm text-red-600 dark:text-red-400">{{ message }}</p>

      <div v-if="targets.length > 1" role="tablist" :aria-label="t('admin.accounts.iqTargets')" class="tabs mb-4 overflow-x-auto">
        <button v-for="target in targets" :key="target.model" type="button" role="tab" :aria-selected="target.model === selected" :data-testid="'iq-tab-' + target.model" class="tab inline-flex shrink-0 items-center gap-2 px-3 py-1.5" :class="target.model === selected && 'tab-active'" @click="selectModel(target.model)">
          <Icon v-if="iqTargetBusy(target)" name="refresh" size="xs" class="animate-spin text-primary-500" />
          <span v-else class="h-2 w-2 rounded-full" :class="dotClass(target)" aria-hidden="true" />
          <span class="max-w-[12rem] truncate">{{ iqModelLabel(target.model) }}</span>
        </button>
      </div>

      <section v-if="current" ref="summary" class="mb-6 border-b border-gray-200 pb-5 dark:border-dark-600" data-testid="iq-target-summary" :aria-label="current.model">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0 space-y-1">
            <p class="flex flex-wrap items-center gap-2">
              <span class="badge" :class="verdictBadge(current)">{{ verdictLabel(current) }}</span>
              <span class="break-all text-sm text-gray-500 dark:text-dark-400">{{ current.model }}</span>
            </p>
            <p class="text-sm text-gray-800 dark:text-gray-200" data-testid="iq-verdict-explain">{{ verdictExplain(current) }}</p>
            <p v-if="current.gate_active && current.gate_model && current.gate_model !== current.model" class="break-all text-xs text-gray-500 dark:text-dark-400" data-testid="iq-gate-model">{{ t('admin.accounts.iqGateModel', { model: current.gate_model }) }}</p>
            <p v-if="stateLine(current)" class="text-sm" :class="current.state === 'paused' ? 'text-amber-700 dark:text-amber-400' : 'text-primary-700 dark:text-primary-300'">{{ stateLine(current) }}</p>
          </div>
          <button type="button" class="btn btn-primary inline-flex shrink-0 items-center gap-2" data-testid="iq-run-target" :disabled="running !== null || iqTargetBusy(current)" @click="run(current.model)">
            <Icon :name="iqTargetBusy(current) || running === current.model ? 'refresh' : 'play'" size="sm" :class="(iqTargetBusy(current) || running === current.model) && 'animate-spin'" />
            {{ t(current.state === 'queued' ? 'admin.accounts.iqStates.queued' : current.state === 'running' ? 'admin.accounts.iqStates.running' : 'admin.accounts.iqRunTarget') }}
          </button>
        </div>
        <dl class="mt-4 grid grid-cols-2 gap-x-5 gap-y-3 text-xs sm:grid-cols-4">
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqLastAssessment') }}</dt><dd class="mt-1 tabular-nums text-gray-900 dark:text-gray-100">{{ current.last_run_at ? formatDateTime(current.last_run_at) : t('admin.accounts.iqNotChecked') }}</dd></div>
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNextCheck') }}</dt><dd class="mt-1 tabular-nums text-gray-900 dark:text-gray-100">{{ nextLabel(current) }}</dd></div>
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqEffort') }}</dt><dd class="mt-1 text-gray-900 dark:text-gray-100">{{ iqEffortLabel(current.reasoning_effort) }}</dd></div>
          <div><dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqInterval') }}</dt><dd class="mt-1 tabular-nums text-gray-900 dark:text-gray-100">{{ current.interval_minutes }}</dd></div>
        </dl>
      </section>

      <div v-if="roundsError" role="alert" class="py-4 text-center">
        <p class="text-sm text-red-600 dark:text-red-400">{{ roundsError }}</p>
        <button type="button" class="btn btn-secondary mt-3" @click="refresh(false)">{{ t('common.refresh') }}</button>
      </div>
      <div v-if="loading && !rounds.length" role="status" class="flex items-center justify-center gap-2 py-12 text-sm text-gray-500 dark:text-dark-400"><Icon name="refresh" size="sm" class="animate-spin" />{{ t('common.loading') }}</div>
      <div v-else-if="!rounds.length && !roundsError" class="py-10 text-center text-gray-500 dark:text-dark-400"><Icon name="clock" class="mx-auto mb-3" /><p class="text-sm">{{ t('admin.accounts.iqEmpty') }}</p></div>

      <ol v-if="rounds.length" class="divide-y divide-gray-200 dark:divide-dark-600">
        <li v-for="round in rounds" :key="round.id" data-testid="iq-round">
          <button type="button" class="flex w-full flex-wrap items-center gap-x-3 gap-y-1 rounded-lg px-2 py-3 text-left hover:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:hover:bg-dark-700/50" :aria-expanded="expanded.has(round.id)" @click="toggle(round.id)">
            <Icon name="chevronRight" size="sm" class="shrink-0 text-gray-400 transition-transform" :class="expanded.has(round.id) && 'rotate-90'" />
            <span class="badge" :class="roundBadge(round)">{{ round.finished_at ? t('admin.accounts.iqCheckStatus.' + (round.status || 'unknown')) : t('admin.accounts.iqStates.running') }}</span>
            <span v-if="round.mode === 'recheck'" class="badge badge-gray">{{ t('admin.accounts.iqRecheck') }}</span>
            <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.accounts.iqAttemptCount', { count: round.attempts.length }) }}</span>
            <span v-if="round.reason && round.reason !== 'correct_answer'" class="min-w-0 truncate text-sm text-gray-500 dark:text-dark-400">{{ reasonLabel(round.reason) }}</span>
            <time :datetime="round.started_at" class="ml-auto text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ formatDateTime(round.started_at) }}</time>
          </button>
          <Transition :css="false" @enter="revealAttempts">
            <ol v-if="expanded.has(round.id)" class="space-y-4 pb-5 pl-2 sm:pl-8" data-testid="iq-attempts">
              <li v-if="round.outbound_model && round.outbound_model !== round.model" class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqOutboundModel', { model: round.outbound_model }) }}</li>
              <li v-for="attempt in round.attempts" :key="attempt.attempt_no" class="iq-attempt rounded-xl bg-gray-50 p-4 dark:bg-dark-800/60" data-testid="iq-attempt">
                <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <span class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.accounts.iqAttemptNumber', { number: attempt.attempt_no }) }}</span>
                  <span class="inline-flex items-center gap-1 text-sm font-medium" :class="outcomeClass(attempt)" data-testid="iq-outcome">
                    <Icon :name="outcomeIcon(attempt)" size="sm" />{{ outcomeLabel(attempt) }}
                  </span>
                  <span class="ml-auto text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ [iqEffortLabel(attempt.effort), attempt.latency_ms != null ? iqSeconds(attempt.latency_ms) : ''].filter(Boolean).join(' · ') }}</span>
                </div>
                <div class="mt-3 grid grid-cols-1 gap-4" :class="attempt.challenge && 'sm:grid-cols-[auto_minmax(0,1fr)]'">
                  <table v-if="attempt.challenge" class="self-start text-sm tabular-nums" data-testid="iq-challenge">
                    <caption class="mb-1 text-left text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqChallengeCaption') }}</caption>
                    <thead>
                      <tr>
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
                  <dl class="grid grid-cols-1 gap-x-5 gap-y-2 text-sm sm:grid-cols-2">
                    <div v-if="attempt.expected" class="min-w-0 sm:col-span-2">
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqExpected') }}</dt>
                      <dd class="mt-0.5 tabular-nums text-gray-900 dark:text-gray-100" data-testid="iq-derivation">{{ attempt.challenge ? t('admin.accounts.iqDerivation', { answer: attempt.expected, circle: attempt.challenge.draw[0], star: attempt.challenge.draw[1] }) : attempt.expected }}</dd>
                    </div>
                    <div v-if="attempt.challenge" class="min-w-0">
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNaive') }}</dt>
                      <dd class="mt-0.5 tabular-nums text-gray-900 dark:text-gray-100">{{ attempt.challenge.naive }}</dd>
                    </div>
                    <div class="min-w-0">
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqNormalizedAnswer') }}</dt>
                      <dd class="mt-0.5 break-words font-semibold tabular-nums text-gray-900 dark:text-gray-100" data-testid="iq-answer">{{ attempt.answer || '—' }}</dd>
                    </div>
                    <div class="min-w-0 sm:col-span-2">
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqReportedModel') }}</dt>
                      <dd class="mt-0.5 flex flex-wrap items-center gap-2" data-testid="iq-reported-model">
                        <span v-if="declarations(attempt).length" class="flex flex-wrap gap-x-3 gap-y-1">
                          <span v-for="item in declarations(attempt)" :key="item.source" class="break-all text-gray-900 dark:text-gray-100"><span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqDeclarationSources.' + item.source) }}</span> {{ item.values.join(', ') }}</span>
                        </span>
                        <span v-else class="break-all text-gray-900 dark:text-gray-100">{{ attempt.reported_model || '—' }}</span>
                        <span v-if="attempt.model_class" class="badge" :class="classBadge(attempt)" data-testid="iq-model-class">{{ classLabel(attempt.model_class) }}</span>
                        <span v-if="attempt.diagnostic?.model_identity?.flags?.includes('source_disagreement')" class="badge badge-warning">{{ t('admin.accounts.iqSourceDisagreement') }}</span>
                      </dd>
                    </div>
                    <div v-if="attempt.status === 'unknown' && attempt.reason" class="min-w-0 sm:col-span-2">
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqReason') }}</dt>
                      <dd class="mt-0.5 text-gray-900 dark:text-gray-100">{{ reasonLabel(attempt.reason) }}</dd>
                    </div>
                    <div v-if="iqTokenSummary(attempt.diagnostic, t)" class="min-w-0 sm:col-span-2">
                      <dt class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.accounts.iqTokenUsage') }}</dt>
                      <dd class="mt-0.5 tabular-nums text-gray-900 dark:text-gray-100">{{ iqTokenSummary(attempt.diagnostic, t) }}</dd>
                    </div>
                  </dl>
                </div>
                <details v-if="iqTechnicalGroups(attempt.diagnostic, t, te).length" class="mt-3" data-testid="iq-technical-details">
                  <summary class="cursor-pointer text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.iqTechnicalDetails') }}</summary>
                  <section v-for="group in iqTechnicalGroups(attempt.diagnostic, t, te)" :key="group.label" class="mt-3">
                    <h4 class="mb-2 text-xs font-medium text-gray-700 dark:text-gray-300">{{ group.label }}</h4>
                    <dl class="grid grid-cols-1 gap-x-5 gap-y-2 text-xs sm:grid-cols-2">
                      <div v-for="row in group.rows" :key="row.label" class="min-w-0">
                        <dt class="text-gray-500 dark:text-dark-400">{{ row.label }}</dt>
                        <dd class="mt-0.5 break-words">{{ row.value }}<code v-if="row.raw && row.raw !== row.value" class="ml-2 break-all text-gray-500 dark:text-dark-400">{{ row.raw }}</code></dd>
                      </div>
                    </dl>
                  </section>
                </details>
              </li>
            </ol>
          </Transition>
        </li>
      </ol>
      <div v-if="nextBefore" class="mt-4 text-center">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loadingMore" @click="loadRounds('more')">{{ t('admin.accounts.iqLoadOlder') }}</button>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { downloadIQCheckDiagnostics } from '@/api/admin/accounts'
import { adminAPI } from '@/api/admin'
import type { Account, IQAttempt, IQRound, IQTarget } from '@/types'
import { formatDateTime } from '@/utils/format'
import { isReducedMotion } from '@/composables/useMotionPreference'
import { IQ_FLAVORS, IQ_SHAPES, iqAttemptOutcome, iqChallengeTable, IQ_CLOCK, iqEffortLabel, iqModelClassTone, iqModelLabel, iqSeconds, iqTargetBusy, iqTechnicalGroups, iqTokenSummary, sortIQTargets } from './iqCheckPresentation'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [targets: IQTarget[]] }>()
const { t, te } = useI18n()
const POLL_MS = 3000

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
function stateLine(target: IQTarget) {
  if (target.state === 'running') return t('admin.accounts.iqRunningProgress', { count: target.completed_attempts })
  if (target.state === 'queued') return t('admin.accounts.iqStates.queued')
  if (target.state === 'paused') return t('admin.accounts.iqPausedBecause', { reason: reasonLabel(target.paused_reason) })
  return ''
}
function nextLabel(target: IQTarget) {
  if (iqTargetBusy(target)) return t('admin.accounts.iqStates.' + target.state)
  if (!target.enabled || target.state === 'paused') return '—'
  const times = [target.next_run_at, target.not_before].filter((value): value is string => !!value)
  return times.length ? formatDateTime(times.reduce((a, b) => (new Date(a) > new Date(b) ? a : b))) : '—'
}

// ---- per-round and per-attempt detail ----
const roundBadge = (round: IQRound) => !round.finished_at ? 'badge-primary' : round.status === 'smart' ? 'badge-success' : round.status === 'degraded' ? 'badge-danger' : 'badge-gray'
const outcomeLabel = (attempt: IQAttempt) => t('admin.accounts.iqOutcomes.' + iqAttemptOutcome(attempt))
const outcomeIcon = (attempt: IQAttempt) => ({ correct: 'checkCircle', wrong: 'xCircle', naive: 'xCircle', downgrade: 'xCircle', conflict: 'exclamationTriangle', unknown: 'questionCircle' } as const)[iqAttemptOutcome(attempt)]
const outcomeClass = (attempt: IQAttempt) => ({ correct: 'text-emerald-700 dark:text-emerald-400', wrong: 'text-red-700 dark:text-red-400', naive: 'text-red-700 dark:text-red-400', downgrade: 'text-red-700 dark:text-red-400', conflict: 'text-amber-700 dark:text-amber-400', unknown: 'text-gray-600 dark:text-dark-300' })[iqAttemptOutcome(attempt)]
const classBadge = (attempt: IQAttempt) => ({ danger: 'badge-danger', warning: 'badge-warning', neutral: 'badge-gray' })[iqModelClassTone(attempt)]
/** Body and header declarations side by side when both sources reported a model. */
function declarations(attempt: IQAttempt) {
  const identity = attempt.diagnostic?.model_identity
  if (!identity?.body?.length || !identity.header?.length) return []
  return [{ source: 'body', values: identity.body }, { source: 'header', values: identity.header }]
}

// ---- data loading and polling ----
const targets = ref<IQTarget[]>([])
const selected = ref('')
const current = computed(() => targets.value.find(target => target.model === selected.value))
const rounds = ref<IQRound[]>([])
const nextBefore = ref<number | null>(null)
const expanded = ref(new Set<number>())
const loading = ref(false)
const loadingMore = ref(false)
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

/** replace: first page; merge: refresh the first page and keep older pages; more: append the next page. */
async function loadRounds(mode: 'replace' | 'merge' | 'more') {
  const id = props.account?.id
  const model = selected.value
  if (!id || !model) return
  const s = session
  const version = mode === 'more' ? roundsVersion : ++roundsVersion
  if (mode === 'more') loadingMore.value = true
  try {
    const page = await adminAPI.accounts.listIQResults(id, { model, before_id: mode === 'more' ? nextBefore.value : null }, controller.signal)
    if (s !== session || version !== roundsVersion || model !== selected.value) return
    roundsError.value = ''
    if (mode === 'merge' && rounds.value.length) {
      const byId = new Map(rounds.value.map(round => [round.id, round]))
      for (const round of page.rounds) byId.set(round.id, round)
      rounds.value = [...byId.values()].sort((a, b) => b.id - a.id)
    } else {
      rounds.value = mode === 'more' ? [...rounds.value, ...page.rounds] : page.rounds
      nextBefore.value = page.next_before_id
    }
  } catch { if (s === session && version === roundsVersion) roundsError.value = t('admin.accounts.iqHistoryFailed') }
  finally { if (mode === 'more') loadingMore.value = false }
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
  await loadRounds(poll ? 'merge' : 'replace')
  if (s !== session) return
  loading.value = false
  schedulePoll()
}

function selectModel(model: string) {
  if (model === selected.value) return
  selected.value = model
  rounds.value = []
  nextBefore.value = null
  expanded.value = new Set()
  roundsError.value = ''
  loading.value = true
  void loadRounds('replace').then(() => { loading.value = false })
}

function toggle(id: number) {
  const next = new Set(expanded.value)
  if (!next.delete(id)) next.add(id)
  expanded.value = next
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
  targets.value = []; selected.value = ''; rounds.value = []; nextBefore.value = null; expanded.value = new Set()
  targetsError.value = ''; roundsError.value = ''; downloadError.value = ''; runMessage.value = ''; runError.value = ''
  loading.value = false; loadingMore.value = false; downloading.value = false; running.value = null
}
watch([() => props.show, () => props.account?.id], () => { reset(); void refresh(false) }, { immediate: true })
onBeforeUnmount(() => { reset(); motion?.revert() })
</script>
