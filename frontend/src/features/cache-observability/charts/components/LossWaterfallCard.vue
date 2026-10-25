<template>
  <SectionShell
    id="loss"
    :title="t('admin.cacheCharts.loss.title')"
    :subtitle="scope"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    :skeleton-rows="7"
    @retry="state.loadStats()"
  >
    <TraceDisabled v-if="data && !data.trace_enabled" @open-settings="emit('open-settings')" />
    <template v-else-if="data">
      <p v-if="truncated" class="mb-3 text-xs text-amber-800 dark:text-amber-300">{{ t('admin.cacheCharts.trace.truncated', { time: utc(data.process_started_at) }) }}</p>
      <p v-if="!steps.length" class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.loss.noOpportunity') }}</p>
      <ol v-else class="space-y-1.5" role="list" data-test="waterfall">
        <li
          v-for="step in steps"
          :key="step.key"
          class="grid grid-cols-[minmax(0,11rem)_minmax(0,1fr)_7.5rem] items-center gap-3 text-sm sm:grid-cols-[minmax(0,15rem)_minmax(0,1fr)_9rem]"
          :data-step="step.key"
        >
          <span class="min-w-0" :class="step.kind === 'part' ? 'pl-3' : 'font-semibold'">
            <span class="block truncate text-gray-900 dark:text-gray-100">{{ stepLabel(step.key) }}</span>
            <code v-if="step.kind === 'part' || step.kind === 'deduct'" class="block truncate text-xs text-gray-500 dark:text-dark-400">{{ step.key }}</code>
          </span>
          <span class="relative h-5" aria-hidden="true">
            <span
              class="absolute inset-y-0 rounded"
              :style="{ left: `${(step.start / steps[0].value) * 100}%`, width: `max(2px, ${((step.end - step.start) / steps[0].value) * 100}%)`, backgroundColor: color(step.color) }"
            />
          </span>
          <span class="text-right tabular-nums text-gray-900 dark:text-gray-100">
            {{ formatTokensM(step.value) }}
            <span class="ml-1 text-xs text-gray-500 dark:text-dark-400">{{ formatPct(step.share, 1) }}</span>
            <span v-if="step.key === 'provider_reasoning_drop_suspect'" class="badge badge-warning ml-1">{{ t('admin.cacheCharts.loss.suspect') }}</span>
          </span>
        </li>
      </ol>

      <div class="mt-5 overflow-x-auto">
        <table class="w-full text-xs" data-test="loss-table">
          <caption class="pb-2 text-left text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.loss.tableCaption') }}</caption>
          <thead>
            <tr class="text-left text-gray-600 dark:text-dark-300">
              <th scope="col" class="py-1.5 pr-3 font-medium">{{ t('admin.cacheCharts.loss.class') }}</th>
              <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.requests') }}</th>
              <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.loss.promptShare') }}</th>
              <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.tokenHit') }}</th>
              <th scope="col" class="py-1.5 text-right font-medium">{{ t('admin.cacheCharts.loss.opportunityShare') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 tabular-nums dark:divide-dark-700">
            <tr v-for="row in rows" :key="row.cls" :class="row.excluded ? 'text-gray-500 dark:text-dark-400' : 'text-gray-800 dark:text-gray-100'">
              <th scope="row" class="py-1.5 pr-3 text-left font-normal">
                {{ className(row.cls) }} <code class="text-gray-500 dark:text-dark-400">{{ row.cls }}</code>
              </th>
              <td class="py-1.5 pr-3 text-right">{{ formatCount(row.requests) }}</td>
              <td class="py-1.5 pr-3 text-right">{{ formatPct(row.promptShare, 1) }}</td>
              <td class="py-1.5 pr-3 text-right">{{ formatPct(row.tokenHit, 1) }}</td>
              <td class="py-1.5 text-right">{{ row.excluded ? t('admin.cacheCharts.loss.excluded') : formatPct(row.opportunityShare, 1) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CacheChartsState } from '../useCacheCharts'
import { formatCount, formatPct, formatTokensM, isTruncated, lossTable, waterfallSteps, type SeriesColor } from '../viewModel'
import SectionShell from './SectionShell.vue'
import TraceDisabled from './TraceDisabled.vue'

const props = defineProps<{ state: CacheChartsState }>()
const emit = defineEmits<{ (e: 'open-settings'): void }>()
const { t, te } = useI18n()

const section = computed(() => props.state.stats)
const data = computed(() => section.value.data)
const steps = computed(() => waterfallSteps(data.value?.loss_classes ?? []))
const rows = computed(() => lossTable(data.value?.loss_classes ?? []))
const truncated = computed(() => (data.value ? isTruncated(data.value) : false))
const dark = computed(() => document.documentElement.classList.contains('dark'))
const scope = computed(() => t('admin.cacheCharts.trace.scope', { window: props.state.filters.traceWindow }))

function color(c: SeriesColor): string {
  return dark.value ? c.dark : c.light
}
function className(cls: string): string {
  const key = `admin.cacheCharts.lossClasses.${cls.replace('/', '_')}`
  return te(key) ? t(key) : cls
}
function stepLabel(key: string): string {
  if (key === 'total') return t('admin.cacheCharts.loss.total')
  if (key === 'attributable') return t('admin.cacheCharts.loss.attributable')
  return className(key)
}
function utc(value: string): string {
  return `${value.slice(0, 16).replace('T', ' ')} UTC`
}
</script>
