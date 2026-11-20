<template>
  <BaseDialog :show="row != null" :title="t('usage.requestDetail.title')" width="normal" @close="emit('close')">
    <div v-if="row" class="divide-y divide-gray-100 text-sm dark:divide-dark-700" data-testid="usage-request-detail">
      <p class="pb-4 tabular-nums text-gray-500 dark:text-gray-400" data-testid="usage-request-detail-time">{{ formatFullLogTime(row.created_at) }}</p>

      <section class="py-4" :aria-labelledby="`${uid}-latency`">
        <h4 :id="`${uid}-latency`" class="mb-2 font-semibold text-gray-900 dark:text-white">{{ t('usage.latency') }}</h4>
        <dl class="space-y-1.5">
          <template v-if="showEndToEnd">
            <div class="flex items-center justify-between gap-4">
              <dt class="text-gray-500 dark:text-gray-400" :title="t('usage.latencyEndToEndHint')">{{ t('usage.latencyEndToEnd') }}</dt>
              <dd v-if="row.end_to_end_first_output_ms != null" data-testid="usage-detail-e2e" class="font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatLatency(row.end_to_end_first_output_ms) }}</dd>
              <dd v-else data-testid="usage-detail-e2e" class="cursor-help text-gray-400 dark:text-gray-500" :title="t('usage.latencyEndToEndUnknownHint')">{{ t('usage.latencyUnknown') }}</dd>
            </div>
            <div class="flex items-center justify-between gap-4">
              <dt class="text-gray-500 dark:text-gray-400" :title="t('usage.latencyLastAttemptHint')">{{ t('usage.latencyLastAttempt') }}</dt>
              <dd v-if="row.first_token_ms != null" data-testid="usage-detail-first-token" class="font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</dd>
              <dd v-else data-testid="usage-detail-first-token" class="cursor-help text-gray-400 dark:text-gray-500" :title="t('usage.latencyLastAttemptUnknownHint')">{{ t('usage.latencyUnknown') }}</dd>
            </div>
          </template>
          <div v-else class="flex items-center justify-between gap-4">
            <dt class="text-gray-500 dark:text-gray-400">{{ t('usage.latencyFirstToken') }}</dt>
            <dd v-if="row.first_token_ms != null" data-testid="usage-detail-first-token" class="font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</dd>
            <dd v-else data-testid="usage-detail-first-token" class="text-gray-400 dark:text-gray-500">—</dd>
          </div>
          <div class="flex items-center justify-between gap-4">
            <dt class="text-gray-500 dark:text-gray-400" :title="showEndToEnd ? t('usage.latencyLastAttemptDurationHint') : undefined">{{ showEndToEnd ? t('usage.latencyLastAttemptDuration') : t('usage.latencyDuration') }}</dt>
            <dd data-testid="usage-detail-duration" class="font-medium tabular-nums" :class="row.duration_ms == null ? 'text-gray-400 dark:text-gray-500' : LATENCY_TEXT_CLASSES[durationSeverity(row.duration_ms)]">{{ formatLatency(row.duration_ms) }}</dd>
          </div>
          <div v-if="showTokenSpeed && hasUsageTokenMetrics(row)" class="flex items-center justify-between gap-4">
            <dt class="text-gray-500 dark:text-gray-400" :title="showEndToEnd ? t('usage.outputTpsHint') : undefined">{{ t('usage.outputTps') }}</dt>
            <dd data-testid="usage-token-speed" class="whitespace-nowrap font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ formatTokenSpeed(row) }}</dd>
          </div>
        </dl>
      </section>

      <section class="py-4" :aria-labelledby="`${uid}-tokens`">
        <h4 :id="`${uid}-tokens`" class="mb-2 font-semibold text-gray-900 dark:text-white">{{ t('usage.tokens') }}</h4>
        <!-- 无账单请求未观测到用量：不编造 0 -->
        <p v-if="unbilled && row.input_tokens == null && row.output_tokens == null" class="text-gray-400 dark:text-gray-500">—</p>
        <dl v-else-if="isImageUsage(row)" class="space-y-1.5">
          <div class="flex items-center justify-between gap-4">
            <dt class="text-gray-500 dark:text-gray-400">{{ t('usage.imageCount') }}</dt>
            <dd class="font-medium tabular-nums text-gray-900 dark:text-gray-100">{{ row.image_count }}{{ t('usage.imageUnit') }} ({{ formatImageBillingSize(row, t) }})</dd>
          </div>
        </dl>
        <UsageTokenBreakdown v-else :row="row" variant="panel" :show-cache-hit-rate="showCacheHitRate" />
      </section>

      <section class="py-4" :aria-labelledby="`${uid}-cost`">
        <h4 :id="`${uid}-cost`" class="mb-2 font-semibold text-gray-900 dark:text-white">{{ t('usage.cost') }}</h4>
        <p v-if="unbilled && row.actual_cost == null" class="text-gray-500 dark:text-gray-400" data-testid="usage-detail-unbilled">
          <span class="text-gray-400 dark:text-gray-500">—</span>
          {{ t('usage.requestDetail.unbilled') }}<template v-if="unbilledWord(row, t)"> · {{ unbilledWord(row, t) }}</template>
        </p>
        <template v-else>
          <p v-if="row.long_context_billing_applied" class="mb-2 text-xs text-amber-700 dark:text-amber-300">{{ t('admin.usage.longContextPricingTooltip') }}</p>
          <UsageCostBreakdown :row="row" variant="panel" :show-account-billing="showAccountBilling" />
        </template>
      </section>

      <section class="pt-4" :aria-labelledby="`${uid}-request`">
        <h4 :id="`${uid}-request`" class="mb-2 font-semibold text-gray-900 dark:text-white">{{ t('usage.requestDetail.request') }}</h4>
        <dl class="space-y-1.5">
          <div class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('usage.model') }}</dt>
            <dd class="min-w-0 break-all text-right font-medium text-gray-900 dark:text-gray-100" data-testid="usage-detail-model">{{ modelSteps(row).join(' → ') }}</dd>
          </div>
          <div v-if="hasModelAudit(row)" class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('usage.upstreamResponseModel') }}</dt>
            <dd class="min-w-0 break-all text-right" :class="isLikelyModelVariant(row) ? 'text-amber-700 dark:text-amber-300' : 'text-orange-700 dark:text-orange-300'">
              {{ row.upstream_response_model }} · {{ isLikelyModelVariant(row) ? t('usage.modelVariant') : t('usage.modelMismatch') }}
            </dd>
          </div>
          <div v-if="row.reasoning_effort" class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('usage.reasoningEffort') }}</dt>
            <dd class="text-right text-gray-900 dark:text-gray-100">
              {{ formatReasoningEffort(row.reasoning_effort) }}<template v-if="hasReasoningEffortMapping(row)"> → {{ formatReasoningEffort(row.upstream_reasoning_effort) }}</template>
            </dd>
          </div>
          <div class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('usage.inboundEndpoint') }}</dt>
            <dd class="min-w-0 break-all text-right" :class="endpoints.inbound ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400 dark:text-gray-500'">{{ endpoints.inbound || '—' }}</dd>
          </div>
          <div v-if="endpoints.upstream != null" class="flex items-baseline justify-between gap-4" data-testid="usage-detail-upstream-endpoint">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('usage.upstreamEndpoint') }}</dt>
            <dd class="min-w-0 break-all text-right" :class="endpoints.upstream ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400 dark:text-gray-500'">{{ endpoints.upstream || '—' }}</dd>
          </div>
          <div class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('admin.usage.group') }}</dt>
            <dd class="min-w-0 text-right">
              <span :class="group ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400 dark:text-gray-500'">{{ group || '—' }}</span>
              <span v-if="crossGroup(row, showFallbackOrigin)" data-testid="usage-detail-fallback" class="ml-1.5 text-amber-700 dark:text-amber-300" :title="t('admin.resilience.usage.fallbackTitle')">
                {{ t('admin.resilience.usage.fallback', { group: row.group?.name || `#${row.group_id}` }) }}
              </span>
            </dd>
          </div>
          <div class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('usage.type') }}</dt>
            <dd class="text-right text-gray-900 dark:text-gray-100">
              {{ requestTypeLabel(row, t) }}<template v-if="row.native_compaction_v2"> · {{ t('usage.nativeCompactionV2') }}</template>
            </dd>
          </div>
          <div class="flex items-baseline justify-between gap-4">
            <dt class="shrink-0 text-gray-500 dark:text-gray-400">{{ t('admin.usage.billingMode') }}</dt>
            <dd class="text-right text-gray-900 dark:text-gray-100">{{ getBillingModeLabel(getDisplayBillingMode(row), t) }}</dd>
          </div>
        </dl>
      </section>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { formatFullLogTime, formatReasoningEffort } from '@/utils/format'
import { LATENCY_TEXT_CLASSES, durationSeverity, firstTokenSeverity } from '@/utils/latencyHealth'
import { formatTokenSpeed, hasUsageTokenMetrics } from '@/utils/usageMetrics'
import { getBillingModeLabel, getDisplayBillingMode, isImageUsage } from '@/utils/billingMode'
import { formatImageBillingSize } from '@/utils/imageUsage'
import UsageTokenBreakdown from './UsageTokenBreakdown.vue'
import UsageCostBreakdown from './UsageCostBreakdown.vue'
import {
  crossGroup,
  endpointPair,
  formatLatency,
  groupLabel,
  hasModelAudit,
  hasReasoningEffortMapping,
  isLikelyModelVariant,
  isUnbilled,
  modelSteps,
  requestTypeLabel,
  unbilledWord,
} from './usageRow'
import type { AdminUsageLog } from '@/types'

/**
 * Everything the table's one-line summaries leave out, for one request: latency, token and cost breakdowns, where it was
 * sent. Opened from each row's "Details" button (click, Enter or Space); the same flags as the table decide what the
 * page may show (account billing, upstream endpoint, fallback origin and user wait are admin-only).
 */
const props = defineProps<{
  row: AdminUsageLog | null
  showEndToEnd: boolean
  showTokenSpeed: boolean
  showCacheHitRate: boolean
  showAccountBilling: boolean
  showUpstreamEndpoint: boolean
  showFallbackOrigin: boolean
}>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const uid = useId()

const unbilled = computed(() => (props.row ? isUnbilled(props.row) : false))
const endpoints = computed(() => (props.row ? endpointPair(props.row, props.showUpstreamEndpoint) : { inbound: '', upstream: null }))
const group = computed(() => (props.row ? groupLabel(props.row, props.showFallbackOrigin) : ''))
</script>
