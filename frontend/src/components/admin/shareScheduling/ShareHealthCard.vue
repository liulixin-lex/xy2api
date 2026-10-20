<template>
  <section class="card p-5" data-testid="share-health">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex min-w-0 items-center gap-3">
        <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.shareScheduling.health.title') }}</h3>
        <span
          v-if="verdict"
          class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-sm font-medium"
          :class="levelClass[verdict.level]"
          :data-testid="'share-health-level-' + verdict.level"
        >
          <span class="h-2 w-2 rounded-full" :class="dotClass[verdict.level]" aria-hidden="true" />
          {{ t('admin.shareScheduling.health.levels.' + verdict.level) }}
        </span>
      </div>
      <div class="flex items-center gap-2 text-sm text-gray-700 dark:text-dark-200">
        <span>{{ t('admin.shareScheduling.health.window') }}</span>
        <Select :model-value="store.reportWindow" class="w-32" :options="windowOptions" :aria-label="t('admin.shareScheduling.health.window')" data-testid="share-health-window" @update:model-value="value => store.setReportWindow(String(value))" />
        <button type="button" class="btn btn-secondary btn-sm" :disabled="store.reportState === 'loading'" data-testid="share-health-refresh" @click="store.loadReport()">
          {{ store.reportState === 'loading' ? t('common.loading') : t('admin.shareScheduling.health.refresh') }}
        </button>
      </div>
    </div>

    <p v-if="store.reportState === 'failed' || store.reportState === 'unavailable'" role="alert" class="mt-3 text-sm text-red-700 dark:text-red-300">
      {{ t('admin.shareScheduling.health.loadFailed') }}<template v-if="store.reportError"> {{ store.reportError }}</template>
    </p>
    <div v-else-if="!report && store.reportState === 'loading'" class="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4" role="status" :aria-label="t('common.loading')">
      <div v-for="cell in 4" :key="cell" class="h-20 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-700" />
    </div>

    <template v-if="report">
      <p v-if="scheduler === 'sub2api'" class="mt-2 text-sm text-gray-600 dark:text-dark-300" data-testid="share-health-sub2api">{{ t('admin.shareScheduling.health.sub2api') }}</p>
      <template v-else>
        <p v-if="verdict?.insufficient === 'window'" class="mt-2 text-sm text-gray-600 dark:text-dark-300" data-testid="share-health-short">{{ t('admin.shareScheduling.health.insufficient.window', { minutes: currentMinutes, min: SHARE_HEALTH_THRESHOLDS.minWindowMinutes }) }}</p>
        <p v-else-if="verdict?.insufficient === 'requests'" class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.health.insufficient.requests', { min: SHARE_HEALTH_THRESHOLDS.minRequests }) }}</p>
        <p v-else-if="verdict?.insufficient === 'baseline'" class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ baselineMissing }}</p>
        <p v-else-if="truncated" class="mt-2 text-sm text-gray-600 dark:text-dark-300" data-testid="share-health-truncated">{{ t('admin.shareScheduling.health.truncated', { minutes: currentMinutes }) }}</p>
        <p v-if="report.baseline && report.baseline_unavailable === 'out_of_retention'" class="mt-2 text-sm text-gray-600 dark:text-dark-300">{{ t('admin.shareScheduling.health.baselineUnavailable.out_of_retention') }}</p>
      </template>
      <p v-if="report.counters_error" role="alert" class="mt-2 text-sm text-amber-700 dark:text-amber-300">{{ t('admin.shareScheduling.health.countersError', { error: report.counters_error }) }}</p>

      <div class="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div v-for="metric in metrics" :key="metric.key" class="rounded-xl border p-3" :class="metricBorder[metric.level]" :data-testid="'share-health-metric-' + metric.key">
          <p class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-dark-400">
            <span v-if="scheduler === 'share'" class="h-1.5 w-1.5 rounded-full" :class="dotClass[metric.level]" aria-hidden="true" />
            {{ t('admin.shareScheduling.health.metrics.' + metric.key) }}
          </p>
          <div class="mt-1 space-y-0.5">
            <div v-for="item in metric.parts" :key="item.key" class="flex flex-wrap items-baseline gap-x-2">
              <span v-if="metric.parts.length > 1" class="w-8 shrink-0 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.shareScheduling.health.parts.' + item.key) }}</span>
              <span class="whitespace-nowrap text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ format(item.current, item.unit) }}</span>
              <span v-if="scheduler === 'share'" class="whitespace-nowrap text-xs tabular-nums text-gray-500 dark:text-dark-400">
                {{ item.baseline === null ? t('admin.shareScheduling.health.noBaseline') : t('admin.shareScheduling.health.vsBaseline', { value: format(item.baseline, item.unit) }) }}
              </span>
            </div>
          </div>
          <p v-if="metric.key === 'cacheHit' && scheduler === 'share'" class="mt-1 text-[11px] leading-snug text-gray-500 dark:text-dark-400" data-testid="share-health-reference-only">{{ t('admin.shareScheduling.health.referenceOnly') }}</p>
        </div>
      </div>

      <p class="mt-3 text-xs text-gray-500 dark:text-dark-400">
        <template v-if="scheduler === 'share' && report.baseline && report.baseline_from && report.baseline_to">{{ t('admin.shareScheduling.health.baseline', { from: formatDateTimeToMinute(report.baseline_from), to: formatDateTimeToMinute(report.baseline_to) }) }} · </template>
        <template v-if="scheduler === 'share' && report.anchor_source === 'policy_updated_at'"><span data-testid="share-health-anchor-estimated">{{ t('admin.shareScheduling.health.anchorEstimated') }}</span> · </template>
        <template v-if="report.counter_precision === 'hour_rounded'">{{ t('admin.shareScheduling.health.hourRounded') }} · </template>
        {{ t('admin.shareScheduling.health.calibrating') }}
      </p>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { useShareSchedulingStore } from '@/stores/shareScheduling'
import { formatDateTimeToMinute } from '@/utils/format'
import { SHARE_HEALTH_THRESHOLDS, judgeShareHealth, type ShareHealthLevel } from '@/utils/shareHealth'
import { formatShareMs, formatSharePercent } from '@/utils/shareScheduling'

const { t } = useI18n()
const store = useShareSchedulingStore()

const windows = [
  { key: 'h1', value: '1h' },
  { key: 'h6', value: '6h' },
  { key: 'h24', value: '24h' }
]
const windowOptions = computed(() => windows.map(w => ({ value: w.value, label: t('admin.shareScheduling.health.windows.' + w.key) })))

const report = computed(() => store.report)
const scheduler = computed(() => store.view?.scheduler ?? report.value?.scheduler ?? 'sub2api')
/** Minutes the current window covers; the server cuts it at the switch. */
const currentMinutes = computed(() => {
  const r = report.value
  if (!r) return 0
  const ms = Date.parse(r.current_to) - Date.parse(r.current_from)
  return Number.isFinite(ms) ? Math.max(Math.floor(ms / 60_000), 0) : 0
})
/** The window is shorter than asked for because the group switched within it. */
const truncated = computed(() => {
  const r = report.value
  return r !== null && r.window_seconds > 0 && currentMinutes.value * 60 < r.window_seconds - 60
})
const judged = computed(() => (report.value ? judgeShareHealth(report.value.current, report.value.baseline, truncated.value ? currentMinutes.value : undefined) : null))
const baselineMissing = computed(() => (report.value?.baseline_unavailable === 'never_switched'
  ? t('admin.shareScheduling.health.baselineUnavailable.never_switched')
  : t('admin.shareScheduling.health.insufficient.baseline')))
/** The original scheduler has nothing to judge: its figures are the future baseline. */
const verdict = computed(() => (scheduler.value === 'share' ? judged.value : null))
const metrics = computed(() => judged.value?.metrics.map(m => (scheduler.value === 'share' ? m : { ...m, level: 'insufficient' as ShareHealthLevel })) ?? [])

const levelClass: Record<ShareHealthLevel, string> = {
  ok: 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300',
  watch: 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300',
  rollback: 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300',
  insufficient: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
}
const dotClass: Record<ShareHealthLevel, string> = {
  ok: 'bg-emerald-500',
  watch: 'bg-amber-500',
  rollback: 'bg-red-500',
  insufficient: 'bg-gray-300 dark:bg-dark-500'
}
const metricBorder: Record<ShareHealthLevel, string> = {
  ok: 'border-gray-200 dark:border-dark-600',
  watch: 'border-amber-300 dark:border-amber-700',
  rollback: 'border-red-300 dark:border-red-700',
  insufficient: 'border-gray-200 dark:border-dark-600'
}

function format(value: number | null, unit: 'ratio' | 'ms'): string {
  if (value === null) return '—'
  return unit === 'ms' ? formatShareMs(value) : formatSharePercent(value, value < 0.1 ? 2 : 1)
}
</script>
