<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { apiClient } from '@/api/client'
const props = defineProps<{ requestId: string; userId?: number | null; apiKeyId?: number | null; groupId?: number | null }>()
interface Attempt {
  account_id: number; priority: number; reason: string; outcome: string
  first_event_ms?: number; first_semantic_ms?: number; overall_first_semantic_ms?: number
  headers_ms?: number; attempt_committed_ms?: number; send_certainty?: string; cancel_reason?: string
}
interface Diagnostics {
  delivery: { metric_version: string; started_at: string; first_flush_at?: string; gateway_read_to_flush_ms?: number; gateway_max_read_to_flush_ms?: number; attempts: Attempt[] }
  recovery?: { state: string; reason?: string; eligible: boolean; unavailable_reason?: string; offline_remaining_ms: number; attachments: number; replay_events?: number; owner_account_id: number; cancel_requested?: boolean; cancel_confirmed?: boolean }
}
const { t } = useI18n()
const loading = ref(false)
const unavailable = ref(false)
const failed = ref(false)
const data = ref<Diagnostics | null>(null)
const last = computed(() => data.value?.delivery.attempts[data.value.delivery.attempts.length - 1])
const unknown = () => t('admin.scheduling.nativeMetrics.unknown')
const stateLabel = (state?: string) => state && ['in_progress', 'client_detached', 'completed', 'incomplete', 'failed', 'cancelled', 'replaying', 'resuming'].includes(state) ? t('admin.scheduling.nativeMetrics.states.' + state) : state || unknown()
const ms = (n?: number) => typeof n === 'number' && Number.isFinite(n) ? n.toLocaleString(undefined, { maximumFractionDigits: 3 }) + ' ms' : unknown()
async function fetchTrace(event?: Event) {
  if (event && !(event.target as HTMLDetailsElement).open) return
  if (loading.value || (event && data.value)) return
  if (!props.userId || !props.apiKeyId) { unavailable.value = true; return }
  loading.value = true; failed.value = false; unavailable.value = false
  try {
    const result = await apiClient.get<Diagnostics>(
      '/admin/ops/requests/' + encodeURIComponent(props.requestId) + '/native-stream',
      { params: { user_id: props.userId, api_key_id: props.apiKeyId, group_id: props.groupId ?? 0 } }
    )
    data.value = result.data
  } catch (e) {
    const error = e as { status?: number; response?: { status?: number } }
    unavailable.value = (error.status ?? error.response?.status) === 404
    failed.value = !unavailable.value
  } finally { loading.value = false }
}
</script>

<template>
  <details class="mt-2 max-w-xl whitespace-normal text-left text-xs" @toggle="fetchTrace">
    <summary class="cursor-pointer font-medium text-gray-700 dark:text-gray-200">{{ t('admin.scheduling.nativeMetrics.title') }}</summary>
    <p v-if="loading" role="status" class="mt-2">{{ t('common.loading') }}</p>
    <p v-else-if="unavailable" class="mt-2 text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.nativeMetrics.legacy') }}</p>
    <div v-else-if="failed" class="mt-2" role="alert"><p>{{ t('admin.scheduling.nativeMetrics.failed') }}</p><button type="button" class="btn btn-secondary mt-2" @click="fetchTrace()">{{ t('common.refresh') }}</button></div>
    <div v-else-if="data" class="mt-3 space-y-3">
      <p class="text-gray-600 dark:text-dark-300">{{ t('admin.scheduling.nativeMetrics.metricHint') }}</p>
      <button type="button" class="btn btn-secondary" @click="fetchTrace()">{{ t('common.refresh') }}</button>
      <dl class="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-2">
        <dt>{{ t('admin.scheduling.nativeMetrics.started') }}</dt><dd class="break-words">{{ data.delivery.started_at }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.headers') }}</dt><dd>{{ ms(last?.headers_ms) }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.firstEvent') }}</dt><dd>{{ ms(last?.first_event_ms) }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.firstContent') }}</dt><dd>{{ ms(last?.first_semantic_ms) }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.overallContent') }}</dt><dd>{{ ms(last?.overall_first_semantic_ms) }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.flush') }}</dt><dd class="break-words">{{ data.delivery.first_flush_at ?? unknown() }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.gatewayDelay') }}</dt><dd>{{ ms(data.delivery.gateway_read_to_flush_ms) }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.attempts') }}</dt><dd>{{ data.delivery.attempts.length || unknown() }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.priority') }}</dt><dd>{{ last?.priority ?? unknown() }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.selection') }}</dt><dd class="break-words">{{ last?.reason || unknown() }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.certainty') }}</dt><dd class="break-words">{{ last?.send_certainty || unknown() }}</dd>
        <dt>{{ t('admin.scheduling.nativeMetrics.finalState') }}</dt><dd class="break-words">{{ stateLabel(data.recovery?.state || last?.outcome) }}</dd>
        <template v-if="data.recovery">
          <dt>{{ t('admin.scheduling.nativeMetrics.recovery') }}</dt><dd>{{ data.recovery.eligible ? t('common.enabled') : t('common.disabled') }}</dd>
          <dt>{{ t('admin.scheduling.nativeMetrics.offline') }}</dt><dd>{{ ms(data.recovery.offline_remaining_ms) }}</dd>
          <dt>{{ t('admin.scheduling.nativeMetrics.attachments') }}</dt><dd>{{ data.recovery.attachments }}</dd>
          <dt>{{ t('admin.scheduling.nativeMetrics.replays') }}</dt><dd>{{ data.recovery.replay_events ?? unknown() }}</dd>
          <template v-if="data.recovery.cancel_requested"><dt>{{ t('admin.scheduling.nativeMetrics.cancelRequested') }}</dt><dd>{{ t('common.yes') }}</dd><dt>{{ t('admin.scheduling.nativeMetrics.cancelConfirmed') }}</dt><dd>{{ data.recovery.cancel_confirmed ? t('common.yes') : t('admin.scheduling.nativeMetrics.unconfirmed') }}</dd></template>
          <dt>{{ t('admin.scheduling.nativeMetrics.reason') }}</dt><dd class="break-words">{{ data.recovery.reason || data.recovery.unavailable_reason || '-' }}</dd>
        </template>
      </dl>
      <ol v-if="data.delivery.attempts.length > 1" class="space-y-2 border-t border-gray-200 pt-3 dark:border-dark-700" :aria-label="t('admin.scheduling.nativeMetrics.attemptHistory')">
        <li v-for="(attempt, index) in data.delivery.attempts" :key="index" class="space-y-1">
          <p class="font-medium">{{ t('admin.scheduling.nativeMetrics.attemptNumber', { number: index + 1 }) }} · {{ stateLabel(attempt.outcome) }}</p>
          <p>{{ t('admin.scheduling.nativeMetrics.firstEvent') }}: {{ ms(attempt.first_event_ms) }} · {{ t('admin.scheduling.nativeMetrics.firstContent') }}: {{ ms(attempt.first_semantic_ms) }}</p>
          <p class="break-words">{{ t('admin.scheduling.nativeMetrics.selection') }}: {{ attempt.reason || unknown() }} · {{ t('admin.scheduling.nativeMetrics.certainty') }}: {{ attempt.send_certainty || unknown() }}</p>
        </li>
      </ol>
    </div>
  </details>
</template>
