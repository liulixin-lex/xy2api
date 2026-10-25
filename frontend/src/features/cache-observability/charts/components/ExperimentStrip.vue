<template>
  <button
    v-if="summary"
    type="button"
    class="flex w-full flex-wrap items-center gap-x-6 gap-y-2 rounded-3xl bg-white px-5 py-4 text-left text-sm shadow-sm ring-1 ring-gray-900/5 transition-colors duration-150 hover:bg-gray-50 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:bg-dark-800 dark:ring-dark-700 dark:hover:bg-dark-700/60 sm:px-6"
    data-test="experiment-strip"
    @click="emit('open')"
  >
    <span class="flex items-center gap-2 font-semibold text-gray-900 dark:text-white">
      <Icon name="beaker" size="sm" aria-hidden="true" />
      {{ summary.experiment.id }} {{ summary.experiment.lever }}
      <span class="badge badge-primary">{{ t('admin.cacheCharts.experiment.status.running') }}</span>
    </span>
    <span class="tabular-nums text-gray-700 dark:text-dark-200">
      {{ t('admin.cacheCharts.experiment.arms', {
        off: summary.off?.sessions ?? EMPTY,
        on: summary.on?.sessions ?? EMPTY,
        planned: summary.experiment.planned_per_arm,
      }) }}
    </span>
    <span v-if="summary.primary" class="tabular-nums text-gray-700 dark:text-dark-200">
      {{ sliceLabel(summary.primary.slice) }}
      {{ summary.primary.ci_ready ? `${formatPp(summary.primary.diff_pp ?? null)} [${formatPpBare(summary.primary.ci_low_pp)}, ${formatPpBare(summary.primary.ci_high_pp)}]` : t('admin.cacheCharts.experiment.ciNotReady') }}
    </span>
    <span class="ml-auto flex items-center gap-1 font-medium" :class="verdictTone">
      {{ verdictText }}
      <Icon name="chevronRight" size="sm" aria-hidden="true" />
    </span>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { Experiment } from '../types'
import { EMPTY, formatPp, runningExperiment } from '../viewModel'

const props = defineProps<{ experiments: Experiment[] }>()
const emit = defineEmits<{ (e: 'open'): void }>()
const { t, te } = useI18n()

const summary = computed(() => runningExperiment(props.experiments))
function sliceLabel(code: string): string {
  const key = `admin.cacheCharts.experiment.slices.${code}`
  return te(key) ? t(key) : code
}
function formatPpBare(value: number | null): string {
  return formatPp(value).replace(/pp$/, '')
}
const verdictText = computed(() => {
  const v = summary.value?.experiment.verdict ?? ''
  const key = `admin.cacheCharts.experiment.verdict.${v}`
  return te(key) ? t(key) : v
})
const verdictTone = computed(() => {
  switch (summary.value?.experiment.verdict) {
    case 'stopped':
    case 'needs_review':
      return 'text-red-700 dark:text-red-300'
    case 'positive':
    case 'safe_only':
      return 'text-emerald-700 dark:text-emerald-300'
    default:
      return 'text-gray-700 dark:text-dark-200'
  }
})
</script>
