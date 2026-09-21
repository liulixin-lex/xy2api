<template>
  <section class="space-y-4 border-t border-gray-200 pt-5 dark:border-dark-600" data-testid="codex-account-ticket-settings">
    <div class="flex items-start justify-between gap-4">
      <div>
        <h3 class="text-sm font-medium text-gray-900 dark:text-gray-100">{{ t('admin.accounts.stateTicket.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.description') }}</p>
      </div>
      <Toggle :model-value="enabled" :disabled="!status || saving" @update:model-value="setEnabled" :aria-label="t('admin.accounts.stateTicket.enable')" data-testid="codex-account-ticket-enabled" />
    </div>
    <p v-if="loading" class="text-xs text-gray-500">{{ t('common.loading') }}</p>
    <template v-if="status">
      <div class="grid items-start gap-4 sm:grid-cols-2">
        <div>
          <label :for="`codex-account-ticket-plan-${accountId}`" class="input-label">{{ t('admin.accounts.stateTicket.plan') }}</label>
          <select :id="`codex-account-ticket-plan-${accountId}`" v-model="ticketPlan" class="input w-full text-sm" :disabled="busy" data-testid="codex-account-ticket-plan">
            <option value="pro">{{ t('admin.accounts.stateTicket.planPro') }}</option>
            <option value="team">{{ t('admin.accounts.stateTicket.planTeam') }}</option>
          </select>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.planHint') }}</p>
        </div>
        <div>
          <label :for="`state-policy-${accountId}`" class="input-label">{{ t('admin.accounts.stateTicket.missingPolicy') }}</label>
          <select :id="`state-policy-${accountId}`" v-model="missingPolicy" :disabled="busy" class="input w-full text-sm" data-testid="state-missing-policy">
            <option value="block">{{ t('admin.accounts.stateTicket.block') }}</option>
            <option value="allow_unprotected">{{ t('admin.accounts.stateTicket.allowUnprotected') }}</option>
          </select>
        </div>
      </div>
      <fieldset :disabled="busy" class="space-y-2">
        <legend class="input-label">{{ t('admin.accounts.stateTicket.models') }}</legend>
        <div class="flex flex-wrap gap-4">
          <label v-for="model in modelOptions" :key="model" class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-200">
            <input v-model="models" type="checkbox" :value="model" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" :data-testid="`state-model-${model}`" />
            {{ model }}
          </label>
        </div>
      </fieldset>
      <p v-if="!status.global_enabled" class="rounded bg-amber-50 p-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="codex-account-ticket-global-off">
        {{ t('admin.accounts.stateTicket.globalOff') }}
        <a href="/admin/settings?tab=gateway" target="_blank" rel="noopener noreferrer" class="font-medium underline">{{ t('admin.accounts.stateTicket.gatewaySettings') }}</a>
      </p>
      <div class="text-xs text-gray-500 dark:text-gray-400" data-testid="codex-account-ticket-global-pool">
        <p v-if="status.proxy_configured">{{ t('admin.accounts.stateTicket.globalPoolConfigured', { address: status.proxy_display }) }}</p>
        <p v-else class="text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.globalPoolMissing') }}</p>
        <p><a href="/admin/settings?tab=gateway" target="_blank" rel="noopener noreferrer" class="font-medium underline">{{ t('admin.accounts.stateTicket.gatewaySettings') }}</a></p>
      </div>
      <div>
        <label :for="`state-manual-proxy-${accountId}`" class="input-label">{{ t('admin.accounts.stateTicket.manualProxy') }}</label>
        <select :id="`state-manual-proxy-${accountId}`" v-model="selectedProxy" class="input w-full text-sm" :disabled="busy">
          <option value="">{{ t('admin.accounts.stateTicket.autoProxy') }}</option>
          <option v-for="proxy in proxyOptions" :key="proxy.id" :value="proxy.id">{{ proxy.name }} · {{ proxy.display }}</option>
        </select>
      </div>
      <div class="divide-y divide-gray-200 dark:divide-dark-600" aria-live="polite" data-testid="state-model-statuses">
        <div v-for="ticket in status.tickets ?? []" :key="ticket.model" class="space-y-2 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="break-all text-sm font-medium text-gray-900 dark:text-white">{{ ticket.model }}</span>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || dirty || proxyChanged || !status.enabled || !status.global_enabled" @click="harvest(ticket.model)">{{ t('admin.accounts.stateTicket.reacquire') }}</button>
          </div>
          <dl class="grid gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-gray-300 sm:grid-cols-3">
            <div><dt class="inline">{{ t('admin.accounts.stateTicket.ticketAvailability') }}: </dt><dd class="inline">{{ ticket.ticket_usable ? t('admin.accounts.stateTicket.ready', { time: formatRemaining(ticket.remaining_seconds) }) : t(`admin.accounts.stateTicket.states.${ticket.state === 'ready' ? 'waiting' : ticket.state}`) }}</dd></div>
            <div><dt class="inline">{{ t('admin.accounts.stateTicket.businessVerification') }}: </dt><dd class="inline" data-testid="state-business-result" :title="businessResult(ticket.last_business_result)">{{ businessSummary(ticket.last_business_result) }}</dd></div>
            <div><dt class="inline">{{ t('admin.accounts.stateTicket.iqResult') }}: </dt><dd class="inline">{{ t(`admin.accounts.stateTicket.iq.${ticket.iq_status || 'unknown'}`) }}<span v-if="ticket.iq_retest === 'queued'"> · {{ t('admin.accounts.stateTicket.iqRetestQueued') }}</span></dd></div>
          </dl>
          <p v-if="ticket.protection && ticket.protection !== 'protected' && ticket.protection !== 'disabled'" class="text-xs text-amber-700 dark:text-amber-300">{{ t(`admin.accounts.stateTicket.protections.${ticket.protection}`) }}</p>
          <p v-if="ticket.state === 'harvesting'" class="text-xs text-gray-600 dark:text-gray-300">{{ ticket.ticket_usable ? t('admin.accounts.stateTicket.refreshing', { time: formatRemaining(ticket.remaining_seconds) }) : t('admin.accounts.stateTicket.attempts', { count: ticket.attempts }) }}</p>
          <p v-if="!ticket.task && (ticket.last_error || ticket.retry_after)" class="break-words text-xs text-amber-700 dark:text-amber-300"><span v-if="ticket.last_error">{{ ticket.last_reason ? failureReason(ticket.last_reason) : ticket.last_error }}</span><span v-if="ticket.retry_after">{{ ticket.last_error ? ' · ' : '' }}{{ t('admin.accounts.stateTicket.retryAfter', { time: formatLocalDate(ticket.retry_after) }) }}</span></p>
          <p v-if="ticket.task && ticket.task.source !== 'quality'" class="text-xs text-gray-600 dark:text-gray-300" data-testid="state-task-status">
            {{ ticket.task.source === 'quality' ? t('admin.accounts.stateTicket.qualityTask') : t(`admin.accounts.stateTicket.tasks.${ticket.task.state}`) }}
            <span v-if="ticket.task.wait_reason"> · {{ taskReason(ticket.task.wait_reason) }}</span>
            <span v-if="ticket.task.retry_at"> · {{ formatLocalDate(ticket.task.retry_at) }}</span>
          </p>
          <p v-if="ticket.quality?.isolated" class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.qualityIsolated') }}</p>
          <p v-if="ticket.standby?.usable" class="text-xs text-emerald-700 dark:text-emerald-400">{{ t('admin.accounts.stateTicket.standbyAvailable') }}</p>
          <details class="text-xs text-gray-600 dark:text-gray-300" data-testid="state-diagnostics">
            <summary class="cursor-pointer py-1 font-medium focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500">{{ t('admin.accounts.stateTicket.diagnostics') }}</summary>
            <div class="mt-2 space-y-2">
          <p v-if="ticket.quality && ticket.quality.mode !== 'disabled'" class="text-xs text-gray-600 dark:text-gray-300" data-testid="state-quality">
            {{ t(ticket.quality.isolated ? 'admin.accounts.stateTicket.qualityIsolated' : 'admin.accounts.stateTicket.qualityObserve') }} · {{ t('admin.accounts.stateTicket.qualityProgress', { count: ticket.quality.completed_questions }) }}
            <span v-if="ticket.quality.latest"> · {{ t('admin.accounts.stateTicket.qualityLatest', { score: ticket.quality.latest.score }) }}</span>
            <span v-if="ticket.quality.last_result === 'unknown'"> · {{ t('admin.accounts.stateTicket.qualityUnknown') }}</span>
          </p>
          <p v-if="ticket.standby?.usable" class="text-xs text-emerald-700 dark:text-emerald-400">{{ t('admin.accounts.stateTicket.standbyReady', { time: formatLocalDate(ticket.standby.expires_at) }) }}</p>
          <p v-if="ticket.budget" class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.backgroundBudget', { used: ticket.budget.calls, limit: ticket.budget.limit }) }}</p>
            </div>
            <dl class="mt-2 grid gap-2 break-words sm:grid-cols-2">
              <div><dt>{{ t('admin.accounts.stateTicket.captureVerification') }}</dt><dd>{{ ticket.model_verified ? t('admin.accounts.stateTicket.verified') : t('admin.accounts.stateTicket.pending') }}<span v-if="ticket.last_replay_at"> · {{ formatLocalDate(ticket.last_replay_at) }}</span></dd></div>
              <div><dt>{{ t('admin.accounts.stateTicket.businessVerification') }}</dt><dd>{{ businessResult(ticket.last_business_result) }}<span v-if="ticket.last_business_at"> · {{ formatLocalDate(ticket.last_business_at) }}</span></dd></div>
              <div v-if="ticket.watchdog.trigger_count"><dt>{{ t('admin.accounts.stateTicket.watchdog') }}</dt><dd>{{ t('admin.accounts.stateTicket.watchdogTriggerCount', { count: ticket.watchdog.trigger_count }) }} · {{ watchdogReason(ticket.watchdog.last_reason || '') }}</dd></div>
              <div v-if="ticket.issued_at"><dt>{{ t('admin.accounts.stateTicket.issuedAt') }}</dt><dd>{{ formatLocalDate(ticket.issued_at) }}</dd></div>
              <div v-if="ticket.first_observed_at"><dt>{{ t('admin.accounts.stateTicket.firstObservedAt') }}</dt><dd>{{ formatLocalDate(ticket.first_observed_at) }}</dd></div>
              <div v-if="ticket.expires_at"><dt>{{ t('admin.accounts.stateTicket.effectiveExpiry') }}</dt><dd>{{ formatLocalDate(ticket.expires_at) }}</dd></div>
              <div v-if="ticket.last_stage"><dt>{{ t('admin.accounts.stateTicket.probeStage') }}</dt><dd>{{ ticket.last_stage === 'replay' ? t('admin.accounts.stateTicket.replayStage') : t('admin.accounts.stateTicket.harvestStage') }}<span v-if="ticket.last_http_status"> · HTTP {{ ticket.last_http_status }}</span></dd></div>
              <div v-if="ticket.observed_length"><dt>{{ t('admin.accounts.stateTicket.observedLength') }}</dt><dd>{{ ticket.observed_length }}</dd></div>
              <div v-if="ticket.last_reason"><dt>{{ t('admin.accounts.stateTicket.failureReason') }}</dt><dd>{{ failureReason(ticket.last_reason) }}</dd></div>
              <div v-if="ticket.counters"><dt>{{ t('admin.accounts.stateTicket.observationCoverage') }}</dt><dd>{{ t('admin.accounts.stateTicket.coverageCounts', { checked: ticket.counters.business_checked ?? 0, unconfirmed: ticket.counters.business_unconfirmed ?? 0 }) }}</dd></div>
            </dl>
            <p class="mt-2">{{ t('admin.accounts.stateTicket.diagnosticHint') }}</p>
          </details>
        </div>
      </div>
      <template v-if="!status.tickets?.length">
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.model', { model: status.model }) }}</p>
      <div class="flex flex-wrap items-center gap-2 text-sm" aria-live="polite" data-testid="codex-account-ticket-status">
        <span :data-testid="refreshingUsable ? 'codex-account-ticket-refreshing-usable' : undefined" :class="status.state === 'ready' || refreshingUsable ? 'text-emerald-700 dark:text-emerald-400' : status.state === 'error' ? 'text-amber-700 dark:text-amber-300' : 'text-gray-600 dark:text-gray-300'">{{ stateLabel }}</span>
        <span v-if="status.state === 'harvesting' && status.attempts" class="text-xs text-gray-500">{{ t('admin.accounts.stateTicket.attempts', { count: status.attempts }) }}</span>
      </div>
      <div v-if="savedTicket" class="space-y-1 rounded bg-emerald-50 p-2 text-xs text-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200" data-testid="codex-account-ticket-saved-ticket">
        <p class="font-medium">{{ t('admin.accounts.stateTicket.savedTicket') }}</p>
        <p v-if="capturedAt || expiresAt" class="flex flex-wrap gap-x-3 gap-y-1">
          <span v-if="capturedAt">{{ t('admin.accounts.stateTicket.capturedAt', { time: capturedAt }) }}</span>
          <span v-if="expiresAt">{{ t('admin.accounts.stateTicket.expiresAt', { time: expiresAt }) }}</span>
        </p>
        <p data-testid="codex-account-ticket-saved-ticket-hint">{{ t('admin.accounts.stateTicket.savedTicketHint') }}</p>
      </div>
      <p v-if="usableAfterRefreshFailure" class="text-xs text-amber-700 dark:text-amber-300" data-testid="codex-account-ticket-usable-error">
        {{ t('admin.accounts.stateTicket.refreshFailedUsable') }}
      </p>
      <p v-if="retryAfter" class="text-xs text-gray-600 dark:text-gray-300" data-testid="codex-account-ticket-retry-after">
        {{ t('admin.accounts.stateTicket.retryAfter', { time: retryAfter }) }}
      </p>
      <div class="space-y-1 rounded bg-gray-50 p-2 text-xs dark:bg-dark-700" aria-live="polite" data-testid="codex-account-ticket-watchdog">
        <p class="flex flex-wrap items-center gap-2">
          <span class="font-medium text-gray-700 dark:text-gray-200">{{ t('admin.accounts.stateTicket.watchdog') }}</span>
          <span :class="status.watchdog.enabled ? 'text-emerald-700 dark:text-emerald-400' : 'text-gray-500 dark:text-gray-400'" data-testid="codex-account-ticket-watchdog-status">{{ status.watchdog.enabled ? t('admin.accounts.stateTicket.watchdogEnabled') : t('admin.accounts.stateTicket.watchdogDisabled') }}</span>
        </p>
        <p class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.watchdogHint') }}</p>
        <p v-if="status.watchdog.trigger_count > 0" class="text-gray-600 dark:text-gray-300" data-testid="codex-account-ticket-watchdog-event">
          {{ t('admin.accounts.stateTicket.watchdogTriggerCount', { count: status.watchdog.trigger_count }) }}
          <span v-if="watchdogLastReason"> · {{ t('admin.accounts.stateTicket.watchdogLastReason', { reason: watchdogLastReason }) }}</span>
          <span v-if="watchdogLastTriggeredAt"> · {{ watchdogLastTriggeredAt }}</span>
        </p>
      </div>
      <p v-if="status.last_error" class="break-words text-xs text-amber-700 dark:text-amber-300" data-testid="codex-account-ticket-error">{{ status.last_error }}</p>
      </template>
      <p v-if="proxyChanged" class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.fixedProxyUnsaved') }}</p>
      <p v-else-if="dirty" class="text-xs text-gray-500">{{ t('admin.accounts.stateTicket.unsaved') }}</p>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy || !dirty || models.length === 0 || proxyChanged || (enabled && !status.proxy_configured)" data-testid="codex-account-ticket-save" @click="save">
          {{ t('admin.accounts.stateTicket.save') }}
        </button>
        <button v-if="!status.tickets?.length || status.tickets.length > 1" type="button" class="btn btn-secondary btn-sm" :disabled="busy || dirty || proxyChanged || !status.global_enabled || !status.enabled || !status.proxy_configured" data-testid="codex-account-ticket-harvest" @click="harvest()">
          {{ (status.tickets?.length ?? 0) > 1 ? t('admin.accounts.stateTicket.acquireAll') : status.state === 'ready' ? t('admin.accounts.stateTicket.reacquire') : t('admin.accounts.stateTicket.acquire') }}
        </button>
      </div>
    </template>
    <p v-if="lastUpdated" class="text-xs text-gray-500 dark:text-gray-400" data-testid="state-last-updated">{{ t('admin.accounts.stateTicket.updatedAt', { time: formatLocalDate(lastUpdated) }) }}</p>
    <p v-if="error" role="alert" class="text-xs text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="saved" role="status" class="text-xs text-emerald-700 dark:text-emerald-400">{{ t('admin.accounts.stateTicket.saved') }}</p>
    <button v-if="!status && !loading" type="button" class="btn btn-secondary btn-sm" @click="load(true)">{{ t('admin.accounts.stateTicket.retry') }}</button>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { getTicketProxyPool, type TicketProxy } from '@/api/admin/codexTicketProxies'
import { getCodexAccountTicket, saveCodexAccountTicket, harvestCodexAccountTicket, type CodexAccountTicketStatus, type CodexTicketPlan } from '@/api/admin/codexTickets'

const props = defineProps<{ accountId: number; visible: boolean; proxyChanged?: boolean }>()
const { t, locale } = useI18n()
const status = ref<CodexAccountTicketStatus | null>(null)
const enabled = ref(false)
const selectedProxy = ref('')
const proxyOptions = ref<TicketProxy[]>([])
const ticketPlan = ref<CodexTicketPlan>('pro')
const models = ref<string[]>(['gpt-6-astra', 'gpt-5.6-sol'])
const missingPolicy = ref<'block' | 'allow_unprotected'>('block')
const modelOptions = computed(() => [...new Set(['gpt-6-astra', 'gpt-5.6-sol', ...(status.value?.models ?? [])])])
function syncDraft(next: CodexAccountTicketStatus) { enabled.value = next.enabled; ticketPlan.value = next.ticket_plan; models.value = [...(next.models ?? [next.model])]; missingPolicy.value = next.missing_policy ?? 'block' }
const loading = ref(false)
const harvesting = ref(false)
const saving = ref(false)
const busy = computed(() => harvesting.value || saving.value)
const error = ref('')
const saved = ref(false)
const lastUpdated = ref('')
let generation = 0
let revision = 0
let timer: ReturnType<typeof setTimeout> | undefined
const dirty = computed(() => !!status.value && (ticketPlan.value !== status.value.ticket_plan || JSON.stringify([...models.value].sort()) !== JSON.stringify([...(status.value.models ?? [status.value.model])].sort()) || missingPolicy.value !== (status.value.missing_policy ?? 'block')))
const hasUsableTicket = computed(() => status.value?.ticket_usable === true)
const refreshingUsable = computed(() => status.value?.state === 'harvesting' && hasUsableTicket.value)
const savedTicket = computed(() => hasUsableTicket.value && !!(capturedAt.value || expiresAt.value))
const usableAfterRefreshFailure = computed(() => hasUsableTicket.value && !!status.value?.last_error)
const remainingTime = computed(() => formatRemaining(status.value?.remaining_seconds ?? 0))
const capturedAt = computed(() => formatLocalDate(status.value?.captured_at))
const expiresAt = computed(() => formatLocalDate(status.value?.expires_at))
const retryAfter = computed(() => formatLocalDate(status.value?.retry_after))
const stateLabel = computed(() => {
  if (!status.value) return ''
  if (refreshingUsable.value) return t('admin.accounts.stateTicket.refreshing', { time: remainingTime.value })
  if (status.value.state === 'ready') {
    return t('admin.accounts.stateTicket.ready', { time: remainingTime.value })
  }
  return t(`admin.accounts.stateTicket.states.${status.value.state}`)
})
const watchdogLastReason = computed(() => {
  const reason = status.value?.watchdog.last_reason
  if (reason === 'model_mismatch') return t('admin.accounts.stateTicket.watchdogModelMismatch')
  if (reason === 'state_312') return t('admin.accounts.stateTicket.watchdogState312')
  return ''
})
const watchdogLastTriggeredAt = computed(() => {
  return formatLocalDate(status.value?.watchdog.last_triggered_at)
})

function watchdogReason(reason: string) {
  if (reason === 'model_mismatch') return t('admin.accounts.stateTicket.watchdogModelMismatch')
  if (reason === 'state_312') return t('admin.accounts.stateTicket.watchdogState312')
  if (reason === 'iq_degraded') return t('admin.accounts.stateTicket.iqDegradedRecovery')
  return ''
}

function formatRemaining(seconds: number) {
  const total = Math.max(0, Math.floor(seconds))
  return `${Math.floor(total / 60)}m ${String(total % 60).padStart(2, '0')}s`
}

function formatLocalDate(value?: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return date.toLocaleString(locale.value, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}
watch([enabled, ticketPlan, models, missingPolicy], () => { saved.value = false }, { flush: 'sync' })

async function load(initial = false) {
  const currentGeneration = generation
  const currentRevision = revision
  if (initial) loading.value = true
  try {
    const next = await getCodexAccountTicket(props.accountId)
    if (generation !== currentGeneration || revision !== currentRevision || !props.visible) return
    // Polling updates status only; it must never overwrite an in-progress edit.
    status.value = next
    enabled.value = next.enabled
    lastUpdated.value = next.updated_at ?? new Date().toISOString()
    if (initial) {
      syncDraft(next)
    }
    error.value = ''
  } catch {
    if (generation === currentGeneration && revision === currentRevision) error.value = t('admin.accounts.stateTicket.loadFailed')
  } finally {
    if (generation === currentGeneration) loading.value = false
  }
}

function schedulePoll() {
  clearTimeout(timer)
  if (!props.visible || document.hidden) return
  timer = setTimeout(async () => {
    const currentGeneration = generation
    if (!props.visible || document.hidden) return
    if (!busy.value) await load()
    if (generation === currentGeneration && props.visible) schedulePoll()
  }, 3000)
}

async function setEnabled(value: boolean) {
  if (!status.value || saving.value) return
  const accountId = props.accountId
  const currentGeneration = generation
  const expected = status.value.config_revision
  revision++
  saving.value = true
  error.value = ''
  saved.value = false
  // The switch always shows the last server-confirmed state while saving.
  try {
    let next: CodexAccountTicketStatus
    try {
      next = await saveCodexAccountTicket(accountId, { enabled: value, expected_revision: expected })
    } catch (cause: unknown) {
      const conflict = ((cause as { status?: number })?.status ?? (cause as { response?: { status?: number } })?.response?.status) === 409
      const live = await getCodexAccountTicket(accountId)
      if (generation !== currentGeneration) return
      status.value = live
      enabled.value = live.enabled
      if (conflict && !value && live.enabled) {
        next = await saveCodexAccountTicket(accountId, { enabled: false, expected_revision: live.config_revision })
      } else if (!conflict && live.enabled === value) {
        next = live
      } else {
        throw cause
      }
    }
    if (generation !== currentGeneration) return
    status.value = next
    enabled.value = next.enabled
    lastUpdated.value = next.updated_at ?? new Date().toISOString()
    saved.value = true
  } catch {
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.saveFailed')
  } finally {
    if (generation === currentGeneration) saving.value = false
  }
}

async function save() {
  if (!status.value || busy.value || props.proxyChanged) return
  saving.value = true
  saved.value = false
  error.value = ''
  revision++
  const currentGeneration = generation
  try {
    const next = await saveCodexAccountTicket(props.accountId, {
      expected_revision: status.value.config_revision,
      ticket_plan: ticketPlan.value,
      models: models.value,
      missing_policy: missingPolicy.value
    })
    if (generation !== currentGeneration) return
    status.value = next
    lastUpdated.value = next.updated_at ?? new Date().toISOString()
    syncDraft(next)
    // Keep success feedback after the draft watchers have cleared the old message.
    saved.value = true
  } catch (cause: unknown) {
    const conflict = ((cause as { status?: number })?.status ?? (cause as { response?: { status?: number } })?.response?.status) === 409
    if (conflict) {
      try {
        const live = await getCodexAccountTicket(props.accountId)
        if (generation === currentGeneration) { status.value = live; enabled.value = live.enabled }
      } catch { /* Keep the last confirmed state and the administrator's draft. */ }
    }
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.saveFailed')
  } finally {
    if (generation === currentGeneration) saving.value = false
  }
}

async function harvest(model?: string) {
  if (busy.value || dirty.value || props.proxyChanged || !status.value?.global_enabled || !status.value.enabled) return
  harvesting.value = true
  saved.value = false
  error.value = ''
  revision++
  const currentGeneration = generation
  const currentRevision = revision
  try {
    const next = await harvestCodexAccountTicket(props.accountId, model, selectedProxy.value || undefined, crypto.randomUUID())
    if (generation === currentGeneration && revision === currentRevision) { status.value = next; lastUpdated.value = next.updated_at ?? new Date().toISOString() }
  } catch {
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.harvestFailed')
  } finally {
    if (generation === currentGeneration) harvesting.value = false
  }
}

watch(() => [props.accountId, props.visible] as const, async () => {
  generation++
  const currentGeneration = generation
  clearTimeout(timer)
  status.value = null
  lastUpdated.value = ''
  enabled.value = false
  selectedProxy.value = ''
  proxyOptions.value = []
  ticketPlan.value = 'pro'
  error.value = ''
  saved.value = false
  harvesting.value = false
  saving.value = false
  loading.value = false
  if (!props.visible) return
  await load(true)
  try {
    const pool = await getTicketProxyPool()
    if (generation === currentGeneration) proxyOptions.value = pool.entries.filter(entry => entry.enabled)
  } catch { /* Account controls remain usable with automatic proxy selection. */ }
  if (generation === currentGeneration && props.visible) schedulePoll()
}, { immediate: true })

function onVisibilityChange() {
  clearTimeout(timer)
  if (!document.hidden && props.visible) schedulePoll()
}
document.addEventListener('visibilitychange', onVisibilityChange)
onBeforeUnmount(() => { generation++; clearTimeout(timer); document.removeEventListener('visibilitychange', onVisibilityChange) })

function businessSummary(result?: string) {
  if (!result || ['verified', 'model_mismatch', 'state_312'].includes(result)) return businessResult(result)
  return t('admin.accounts.stateTicket.unconfirmed')
}
function businessResult(result?: string) {
  const known = ['verified', 'model_mismatch', 'state_312', 'oversized', 'unsupported_encoding', 'upstream_failed', 'unconfirmed']
  return t(`admin.accounts.stateTicket.businessResults.${result && known.includes(result) ? result : 'not_observed'}`)
}
function taskReason(reason: string) {
  const known = ['account_cooldown', 'account_health', 'attempt_interval', 'execution_slot', 'budget', 'proxy_unavailable', 'account_concurrency', 'upstream_capacity']
  return t(`admin.accounts.stateTicket.taskReasons.${known.includes(reason) ? reason : 'execution_slot'}`)
}
function failureReason(reason: string) {
  const known = ['rate_limited', 'auth_rejected', 'model_capacity', 'response_failed', 'invalid_state_header', 'invalid_ticket_time', 'model_mismatch']
  return t(`admin.accounts.stateTicket.failureReasons.${known.includes(reason) ? reason : 'unconfirmed'}`)
}
</script>
