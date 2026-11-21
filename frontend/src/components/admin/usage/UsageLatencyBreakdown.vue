<template>
  <dl class="space-y-1.5">
    <template v-if="showEndToEnd">
      <div class="flex items-center justify-between gap-4">
        <dt :class="LABEL" :title="t('usage.latencyEndToEndHint')">{{ t('usage.latencyEndToEnd') }}</dt>
        <dd v-if="row.end_to_end_first_output_ms != null" data-testid="usage-detail-e2e" :class="VALUE">{{ formatLatency(row.end_to_end_first_output_ms) }}</dd>
        <dd v-else data-testid="usage-detail-e2e" :class="UNKNOWN" :title="t('usage.latencyEndToEndUnknownHint')">{{ t('usage.latencyUnknown') }}</dd>
      </div>
      <div class="flex items-center justify-between gap-4">
        <dt :class="LABEL" :title="t('usage.latencyLastAttemptHint')">{{ t('usage.latencyLastAttempt') }}</dt>
        <dd v-if="row.first_token_ms != null" data-testid="usage-detail-first-token" class="font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</dd>
        <dd v-else data-testid="usage-detail-first-token" :class="UNKNOWN" :title="t('usage.latencyLastAttemptUnknownHint')">{{ t('usage.latencyUnknown') }}</dd>
      </div>
    </template>
    <div v-else class="flex items-center justify-between gap-4">
      <dt :class="LABEL">{{ t('usage.latencyFirstToken') }}</dt>
      <dd v-if="row.first_token_ms != null" data-testid="usage-detail-first-token" class="font-medium tabular-nums" :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</dd>
      <dd v-else data-testid="usage-detail-first-token" :class="EMPTY">—</dd>
    </div>
    <div class="flex items-center justify-between gap-4">
      <dt :class="LABEL" :title="showEndToEnd ? t('usage.latencyLastAttemptDurationHint') : undefined">{{ showEndToEnd ? t('usage.latencyLastAttemptDuration') : t('usage.latencyDuration') }}</dt>
      <dd data-testid="usage-detail-duration" class="font-medium tabular-nums" :class="row.duration_ms == null ? EMPTY : LATENCY_TEXT_CLASSES[durationSeverity(row.duration_ms)]">{{ formatLatency(row.duration_ms) }}</dd>
    </div>
    <div v-if="showTokenSpeed && hasUsageTokenMetrics(row)" class="flex items-center justify-between gap-4">
      <dt :class="LABEL" :title="showEndToEnd ? t('usage.outputTpsHint') : undefined">{{ t('usage.outputTps') }}</dt>
      <dd data-testid="usage-token-speed" class="whitespace-nowrap" :class="VALUE">{{ formatTokenSpeed(row) }}</dd>
    </div>
  </dl>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { LATENCY_TEXT_CLASSES, durationSeverity, firstTokenSeverity } from '@/utils/latencyHealth'
import { formatTokenSpeed, hasUsageTokenMetrics } from '@/utils/usageMetrics'
import { formatLatency } from './usageRow'
import type { AdminUsageLog } from '@/types'

/**
 * A usage row's latency in the request detail dialog, with the full names: on the admin page the user wait, the last
 * attempt's first token and duration; on the user page first token and total duration; average TPS where the setting
 * shows it. The explanations ride on the labels.
 */
withDefaults(defineProps<{
  row: AdminUsageLog
  showEndToEnd?: boolean
  showTokenSpeed?: boolean
}>(), {
  showEndToEnd: false,
  showTokenSpeed: false,
})

const { t } = useI18n()

const LABEL = 'text-gray-500 dark:text-gray-400'
const VALUE = 'font-medium tabular-nums text-gray-900 dark:text-gray-100'
const UNKNOWN = 'cursor-help text-gray-400 dark:text-gray-500'
const EMPTY = 'text-gray-400 dark:text-gray-500'
</script>
