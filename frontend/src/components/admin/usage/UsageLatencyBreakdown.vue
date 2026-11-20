<template>
  <div :data-variant="variant">
    <div v-if="variant === 'tooltip'" class="mb-1 text-xs font-semibold text-gray-300">{{ t('usage.latencyDetails') }}</div>
    <dl class="space-y-1.5">
      <template v-if="showEndToEnd">
        <div class="flex items-center justify-between gap-4">
          <dt :class="tone.label" :title="hint('usage.latencyEndToEndHint')">{{ t('usage.latencyEndToEnd') }}</dt>
          <dd v-if="row.end_to_end_first_output_ms != null" data-testid="usage-detail-e2e" :class="tone.value">{{ formatLatency(row.end_to_end_first_output_ms) }}</dd>
          <dd v-else data-testid="usage-detail-e2e" :class="tone.unknown" :title="hint('usage.latencyEndToEndUnknownHint')">{{ t('usage.latencyUnknown') }}</dd>
        </div>
        <div class="flex items-center justify-between gap-4">
          <dt :class="tone.label" :title="hint('usage.latencyLastAttemptHint')">{{ t('usage.latencyLastAttempt') }}</dt>
          <dd v-if="row.first_token_ms != null" data-testid="usage-detail-first-token" class="font-medium tabular-nums" :class="severity[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</dd>
          <dd v-else data-testid="usage-detail-first-token" :class="tone.unknown" :title="hint('usage.latencyLastAttemptUnknownHint')">{{ t('usage.latencyUnknown') }}</dd>
        </div>
      </template>
      <div v-else class="flex items-center justify-between gap-4">
        <dt :class="tone.label">{{ t('usage.latencyFirstToken') }}</dt>
        <dd v-if="row.first_token_ms != null" data-testid="usage-detail-first-token" class="font-medium tabular-nums" :class="severity[firstTokenSeverity(row.first_token_ms)]">{{ formatLatency(row.first_token_ms) }}</dd>
        <dd v-else data-testid="usage-detail-first-token" :class="tone.empty">—</dd>
      </div>
      <div class="flex items-center justify-between gap-4">
        <dt :class="tone.label" :title="showEndToEnd ? hint('usage.latencyLastAttemptDurationHint') : undefined">{{ showEndToEnd ? t('usage.latencyLastAttemptDuration') : t('usage.latencyDuration') }}</dt>
        <dd data-testid="usage-detail-duration" class="font-medium tabular-nums" :class="row.duration_ms == null ? tone.empty : severity[durationSeverity(row.duration_ms)]">{{ formatLatency(row.duration_ms) }}</dd>
      </div>
      <div v-if="showTokenSpeed && hasUsageTokenMetrics(row)" class="flex items-center justify-between gap-4">
        <dt :class="tone.label" :title="showEndToEnd ? hint('usage.outputTpsHint') : undefined">{{ t('usage.outputTps') }}</dt>
        <dd data-testid="usage-token-speed" class="whitespace-nowrap" :class="tone.value">{{ formatTokenSpeed(row) }}</dd>
      </div>
    </dl>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { LATENCY_TEXT_CLASSES, durationSeverity, firstTokenSeverity, type LatencySeverity } from '@/utils/latencyHealth'
import { formatTokenSpeed, hasUsageTokenMetrics } from '@/utils/usageMetrics'
import { formatLatency } from './usageRow'
import type { AdminUsageLog } from '@/types'

/**
 * A usage row's latency, the same lines and rules for the table's hover tip on desktop (dark tooltip) and the request
 * detail dialog (themed panel): on the admin page the user wait, the last attempt's first token and duration; on the
 * user page first token and total duration; average TPS where the setting shows it. The explanations ride on the
 * labels in the panel only (a tooltip cannot be hovered).
 */
const props = withDefaults(defineProps<{
  row: AdminUsageLog
  variant?: 'tooltip' | 'panel'
  showEndToEnd?: boolean
  showTokenSpeed?: boolean
}>(), {
  variant: 'tooltip',
  showEndToEnd: false,
  showTokenSpeed: false,
})

const { t } = useI18n()

const TONES = {
  tooltip: {
    label: 'text-gray-400',
    value: 'font-medium tabular-nums text-white',
    unknown: 'text-gray-400',
    empty: 'text-gray-400',
  },
  panel: {
    label: 'text-gray-500 dark:text-gray-400',
    value: 'font-medium tabular-nums text-gray-900 dark:text-gray-100',
    unknown: 'cursor-help text-gray-400 dark:text-gray-500',
    empty: 'text-gray-400 dark:text-gray-500',
  },
} as const

/** The tooltip is dark in both themes: its grades use the dark theme's lighter shades. */
const TOOLTIP_SEVERITY: Record<LatencySeverity, string> = {
  good: 'text-emerald-400',
  warn: 'text-amber-400',
  slow: 'text-orange-400',
  critical: 'text-red-400',
}

const tone = computed(() => TONES[props.variant])
const severity = computed(() => (props.variant === 'tooltip' ? TOOLTIP_SEVERITY : LATENCY_TEXT_CLASSES))
const hint = (key: string) => (props.variant === 'panel' ? t(key) : undefined)
</script>
