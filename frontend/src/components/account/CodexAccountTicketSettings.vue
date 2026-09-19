<template>
  <section class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="codex-account-ticket-settings">
    <div class="flex items-start justify-between gap-4">
      <div>
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.accounts.stateTicket.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.description') }}</p>
      </div>
      <Toggle v-model="enabled" :disabled="!status || busy" :aria-label="t('admin.accounts.stateTicket.enable')" data-testid="codex-account-ticket-enabled" />
    </div>
    <p v-if="loading" class="text-xs text-gray-500">{{ t('common.loading') }}</p>
    <template v-if="status">
      <div>
        <label :for="`codex-account-ticket-plan-${accountId}`" class="input-label">{{ t('admin.accounts.stateTicket.plan') }}</label>
        <select :id="`codex-account-ticket-plan-${accountId}`" v-model="ticketPlan" class="input w-full text-sm" :disabled="busy" data-testid="codex-account-ticket-plan">
          <option value="pro">{{ t('admin.accounts.stateTicket.planPro') }}</option>
          <option value="team">{{ t('admin.accounts.stateTicket.planTeam') }}</option>
        </select>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.planHint') }}</p>
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
      <div>
        <label :for="`state-policy-${accountId}`" class="input-label">{{ t('admin.accounts.stateTicket.missingPolicy') }}</label>
        <select :id="`state-policy-${accountId}`" v-model="missingPolicy" :disabled="busy" class="input w-full text-sm" data-testid="state-missing-policy">
          <option value="block">{{ t('admin.accounts.stateTicket.block') }}</option>
          <option value="allow_unprotected">{{ t('admin.accounts.stateTicket.allowUnprotected') }}</option>
        </select>
      </div>
      <p v-if="!status.global_enabled" class="rounded bg-amber-50 p-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-200" data-testid="codex-account-ticket-global-off">
        {{ t('admin.accounts.stateTicket.globalOff') }}
        <a href="/admin/settings?tab=gateway" target="_blank" rel="noopener noreferrer" class="font-medium underline">{{ t('admin.accounts.stateTicket.gatewaySettings') }}</a>
      </p>
      <div class="text-xs text-gray-500 dark:text-gray-400" data-testid="codex-account-ticket-global-pool">
        <p v-if="status.proxy_configured">{{ t('admin.accounts.stateTicket.globalPoolConfigured', { address: status.proxy_display }) }}</p>
        <p v-else class="text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.globalPoolMissing') }}</p>
        <p>{{ t('admin.accounts.stateTicket.globalPoolHint') }} <a href="/admin/settings?tab=gateway" target="_blank" rel="noopener noreferrer" class="font-medium underline">{{ t('admin.accounts.stateTicket.gatewaySettings') }}</a></p>
      </div>
      <div class="divide-y divide-gray-200 dark:divide-dark-600" aria-live="polite" data-testid="state-model-statuses">
        <div v-for="ticket in status.tickets ?? []" :key="ticket.model" class="space-y-2 py-3">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="break-all text-sm font-medium text-gray-900 dark:text-white">{{ ticket.model }}</span>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || dirty || proxyChanged || !status.enabled || !status.global_enabled || ticket.state === 'harvesting' || coolingDown(ticket)" @click="harvest(ticket.model)">{{ t('admin.accounts.stateTicket.reacquire') }}</button>
          </div>
          <dl class="grid gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-gray-300 sm:grid-cols-2">
            <div><dt class="inline">{{ t('admin.accounts.stateTicket.ticketAvailability') }}: </dt><dd class="inline">{{ ticket.ticket_usable ? t('admin.accounts.stateTicket.ready', { time: formatRemaining(ticket.remaining_seconds) }) : t('admin.accounts.stateTicket.states.waiting') }}</dd></div>
            <div><dt class="inline">{{ t('admin.accounts.stateTicket.modelVerification') }}: </dt><dd class="inline">{{ ticket.model_verified ? t('admin.accounts.stateTicket.verified') : t('admin.accounts.stateTicket.pending') }}</dd></div>
            <div><dt class="inline">{{ t('admin.accounts.stateTicket.iqResult') }}: </dt><dd class="inline">{{ t(`admin.accounts.stateTicket.iq.${ticket.iq_status || 'unknown'}`) }}</dd></div>
            <div v-if="ticket.protection"><dt class="inline">{{ t('admin.accounts.stateTicket.protection') }}: </dt><dd class="inline">{{ t(`admin.accounts.stateTicket.protections.${ticket.protection}`) }}</dd></div>
          </dl>
          <p v-if="ticket.ticket_usable && ticket.expires_at" class="text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.stateTicket.expiresAt', { time: formatLocalDate(ticket.expires_at) }) }}</p>
          <p v-if="ticket.state === 'harvesting'" class="text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.stateTicket.states.harvesting') }} · {{ t('admin.accounts.stateTicket.attempts', { count: ticket.attempts }) }}</p>
          <p v-if="ticket.watchdog.trigger_count > 0" class="text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.stateTicket.watchdogTriggerCount', { count: ticket.watchdog.trigger_count }) }}<span v-if="ticket.watchdog.last_reason"> · {{ watchdogReason(ticket.watchdog.last_reason) }}</span></p>
          <p v-if="ticket.refreshing" class="text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.stateTicket.refreshing', { time: formatRemaining(ticket.remaining_seconds) }) }}</p>
          <p v-if="ticket.iq_retest === 'queued'" class="text-xs text-gray-600 dark:text-gray-300">{{ t('admin.accounts.stateTicket.iqRetestQueued') }}</p>
          <p v-if="ticket.retry_after" class="text-xs text-amber-700 dark:text-amber-300">{{ t('admin.accounts.stateTicket.retryAfter', { time: formatLocalDate(ticket.retry_after) }) }}</p>
          <p v-if="ticket.last_error" class="break-words text-xs text-amber-700 dark:text-amber-300">{{ ticket.last_error }}</p>
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
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy || dirty || proxyChanged || !status.global_enabled || !status.enabled || !status.proxy_configured || (status.state === 'harvesting' || coolingDown(status))" data-testid="codex-account-ticket-harvest" @click="harvest()">
          {{ (status.tickets?.length ?? 0) > 1 ? t('admin.accounts.stateTicket.acquireAll') : status.state === 'ready' ? t('admin.accounts.stateTicket.reacquire') : t('admin.accounts.stateTicket.acquire') }}
        </button>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.stateTicket.failureHint') }}</p>
    </template>
    <p v-if="error" role="alert" class="text-xs text-red-600 dark:text-red-400">{{ error }}</p>
    <p v-if="saved" role="status" class="text-xs text-emerald-700 dark:text-emerald-400">{{ t('admin.accounts.stateTicket.saved') }}</p>
    <button v-if="!status && !loading" type="button" class="btn btn-secondary btn-sm" @click="load(true)">{{ t('admin.accounts.stateTicket.retry') }}</button>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { getCodexAccountTicket, saveCodexAccountTicket, harvestCodexAccountTicket, type CodexAccountTicketStatus, type CodexTicketPlan } from '@/api/admin/codexTickets'

const props = defineProps<{ accountId: number; visible: boolean; proxyChanged?: boolean }>()
const { t, locale } = useI18n()
const status = ref<CodexAccountTicketStatus | null>(null)
const enabled = ref(false)
const ticketPlan = ref<CodexTicketPlan>('pro')
const models = ref<string[]>(['gpt-6-astra', 'gpt-5.6-sol'])
const missingPolicy = ref<'block' | 'allow_unprotected'>('block')
const modelOptions = computed(() => [...new Set(['gpt-6-astra', 'gpt-5.6-sol', ...(status.value?.models ?? [])])])
function coolingDown(ticket: CodexAccountTicketStatus) { return !!ticket.retry_after && new Date(ticket.retry_after).getTime() > Date.now() }
function syncDraft(next: CodexAccountTicketStatus) { enabled.value = next.enabled; ticketPlan.value = next.ticket_plan; models.value = [...(next.models ?? [next.model])]; missingPolicy.value = next.missing_policy ?? 'block' }
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const saved = ref(false)
let generation = 0
let revision = 0
let timer: ReturnType<typeof setTimeout> | undefined
const dirty = computed(() => !!status.value && (enabled.value !== status.value.enabled || ticketPlan.value !== status.value.ticket_plan || JSON.stringify([...models.value].sort()) !== JSON.stringify([...(status.value.models ?? [status.value.model])].sort()) || missingPolicy.value !== (status.value.missing_policy ?? 'block')))
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
  timer = setTimeout(async () => {
    const currentGeneration = generation
    if (!props.visible) return
    if (!busy.value) await load()
    if (generation === currentGeneration && props.visible) schedulePoll()
  }, 3000)
}

async function save() {
  if (!status.value || busy.value || props.proxyChanged) return
  busy.value = true
  saved.value = false
  error.value = ''
  revision++
  const currentGeneration = generation
  try {
    const next = await saveCodexAccountTicket(props.accountId, {
      enabled: enabled.value,
      ticket_plan: ticketPlan.value,
      models: models.value,
      missing_policy: missingPolicy.value
    })
    if (generation !== currentGeneration) return
    status.value = next
    syncDraft(next)
    // Keep success feedback after the draft watchers have cleared the old message.
    saved.value = true
  } catch {
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.saveFailed')
  } finally {
    if (generation === currentGeneration) busy.value = false
  }
}

async function harvest(model?: string) {
  if (busy.value || dirty.value || props.proxyChanged || !status.value?.global_enabled || !status.value.enabled) return
  busy.value = true
  saved.value = false
  error.value = ''
  revision++
  const currentGeneration = generation
  try {
    const next = await harvestCodexAccountTicket(props.accountId, model)
    if (generation === currentGeneration) status.value = next
  } catch {
    if (generation === currentGeneration) error.value = t('admin.accounts.stateTicket.harvestFailed')
  } finally {
    if (generation === currentGeneration) busy.value = false
  }
}

watch(() => [props.accountId, props.visible] as const, async () => {
  generation++
  const currentGeneration = generation
  clearTimeout(timer)
  status.value = null
  enabled.value = false
  ticketPlan.value = 'pro'
  error.value = ''
  saved.value = false
  busy.value = false
  loading.value = false
  if (!props.visible) return
  await load(true)
  if (generation === currentGeneration && props.visible) schedulePoll()
}, { immediate: true })

onBeforeUnmount(() => { generation++; clearTimeout(timer) })
</script>
