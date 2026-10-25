<template>
  <SectionShell
    id="coverage"
    :title="t('admin.cacheCharts.coverage.title')"
    :subtitle="t('admin.cacheCharts.trace.scope', { window: state.filters.traceWindow })"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    @retry="state.loadStats()"
  >
    <template #actions><ScopeTags :tags="scope?.bridgeOnly ? ['bridgeOnly'] : []" /></template>
    <TraceDisabled v-if="data && !data.trace_enabled" @open-settings="emit('open-settings')" />
    <template v-else-if="data">
      <p
        v-if="scope?.notCovered"
        class="mb-4 flex items-start gap-2 rounded-xl bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:bg-amber-900/20 dark:text-amber-100"
        data-test="coverage-not-covered"
      >
        <Icon name="infoCircle" size="sm" class="mt-0.5 shrink-0" aria-hidden="true" />
        {{ t('admin.cacheCharts.coverage.notes.platform_not_covered') }}
      </p>
      <dl class="space-y-3 text-sm">
        <div v-for="metric in metrics" :key="metric.key" class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <dt class="flex flex-wrap items-center gap-2 text-gray-600 dark:text-dark-300">
            {{ t(`admin.cacheCharts.coverage.${metric.key}`) }}
            <ScopeTags v-if="metric.openaiOnly" :tags="['openaiOnly']" />
          </dt>
          <dd class="flex items-center gap-2 tabular-nums">
            <span class="text-lg font-semibold text-gray-900 dark:text-white">{{ formatPct(metric.value, 1) }}</span>
            <span class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.coverage.target', { target: formatPct(metric.target, 0) }) }}</span>
            <span v-if="metric.value !== null && metric.value < metric.target" class="inline-flex items-center gap-1 text-xs text-amber-800 dark:text-amber-300" :data-test="`below-${metric.key}`">
              <Icon name="exclamationTriangle" size="xs" aria-hidden="true" />
              {{ t('admin.cacheCharts.coverage.below') }}
            </span>
          </dd>
        </div>
      </dl>

      <div v-if="segments" class="mt-5">
        <div class="flex h-3 w-full gap-0.5 overflow-hidden rounded" aria-hidden="true">
          <span class="h-full rounded-sm bg-primary-600" :style="{ width: `${segments.judgeable * 100}%` }" />
          <span class="h-full rounded-sm bg-gray-400 dark:bg-dark-400" :style="{ width: `${segments.excluded * 100}%` }" />
          <span class="h-full rounded-sm bg-gray-200 dark:bg-dark-600" :style="{ width: `${segments.untraced * 100}%` }" />
        </div>
        <ul class="mt-2 space-y-1 text-xs text-gray-700 dark:text-dark-200" role="list">
          <li class="flex items-center justify-between gap-3">
            <span class="flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-primary-600" aria-hidden="true" />{{ t('admin.cacheCharts.coverage.judgeable') }}</span>
            <span class="tabular-nums">{{ formatPct(segments.judgeable, 1) }}</span>
          </li>
          <li class="flex items-center justify-between gap-3">
            <span class="flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-gray-400 dark:bg-dark-400" aria-hidden="true" />{{ t('admin.cacheCharts.coverage.excluded') }}</span>
            <span class="tabular-nums">{{ formatPct(segments.excluded, 1) }}</span>
          </li>
          <li v-for="item in segments.excludedBreakdown" :key="item.cls" class="flex items-center justify-between gap-3 pl-3.5 text-gray-500 dark:text-dark-400">
            <code>{{ item.cls }}</code>
            <span class="tabular-nums">{{ formatPct(item.share, 2) }}<span class="ml-2">{{ t('admin.cacheCharts.coverage.requests', { n: formatCount(item.requests) }) }}</span></span>
          </li>
          <li class="flex items-center justify-between gap-3">
            <span class="flex items-center gap-1.5"><span class="h-2 w-2 rounded-sm bg-gray-200 dark:bg-dark-600" aria-hidden="true" />{{ t('admin.cacheCharts.coverage.untraced') }}</span>
            <span class="tabular-nums">{{ formatPct(segments.untraced, 1) }}</span>
          </li>
        </ul>
      </div>

      <ul class="mt-4 space-y-1 text-xs text-gray-500 dark:text-dark-400" role="list">
        <li v-for="note in notes" :key="note">{{ te(`admin.cacheCharts.coverage.notes.${note}`) ? t(`admin.cacheCharts.coverage.notes.${note}`) : note }}</li>
        <li v-if="truncated">{{ t('admin.cacheCharts.trace.truncated', { time: `${data.process_started_at.slice(0, 16).replace('T', ' ')} UTC` }) }}</li>
        <li v-if="typeof data.trace_policy_rev === 'number'">{{ t('admin.cacheCharts.coverage.policyRev', { rev: data.trace_policy_rev }) }}</li>
      </ul>
    </template>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { CacheChartsState } from '../useCacheCharts'
import { coverageSegments, formatCount, formatPct, isTruncated, statsScope } from '../viewModel'
import ScopeTags from './ScopeTags.vue'
import SectionShell from './SectionShell.vue'
import TraceDisabled from './TraceDisabled.vue'

const props = defineProps<{ state: CacheChartsState }>()
const emit = defineEmits<{ (e: 'open-settings'): void }>()
const { t, te } = useI18n()

const section = computed(() => props.state.stats)
const data = computed(() => section.value.data)
const segments = computed(() => (data.value ? coverageSegments(data.value) : null))
const truncated = computed(() => (data.value ? isTruncated(data.value) : false))
const scope = computed(() => (data.value ? statsScope(data.value) : null))
// The callout above says it; the list does not repeat it.
const notes = computed(() => (data.value?.scope_notes ?? []).filter((n) => n !== 'platform_not_covered'))
const metrics = computed(() => {
  const c = data.value?.coverage
  if (!c) return []
  const openaiOnly = !!scope.value?.openaiOnly
  return [
    { key: 'traceCoverage', value: c.coverage_ratio, target: c.targets.coverage_ratio, openaiOnly: false },
    // OpenAI only (M2b §1.3): marked as such, and left out of a provider view where it is null.
    { key: 'categoryKnown', value: c.category_known_ratio, target: c.targets.category_known_ratio, openaiOnly },
  ].filter((m) => !(m.openaiOnly && m.value === null))
})
</script>
