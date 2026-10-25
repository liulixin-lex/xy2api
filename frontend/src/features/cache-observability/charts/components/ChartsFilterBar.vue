<template>
  <div class="rounded-3xl bg-white p-4 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:px-6" data-test="charts-filters">
    <div class="flex flex-wrap items-end gap-x-6 gap-y-3">
      <fieldset class="min-w-0">
        <legend class="mb-1 text-xs font-medium text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.filters.summaryWindow') }}</legend>
        <div class="tabs inline-flex" role="radiogroup" :aria-label="t('admin.cacheCharts.filters.summaryWindow')">
          <button
            v-for="w in summaryWindows"
            :key="w"
            type="button"
            role="radio"
            class="tab"
            :class="state.filters.summaryWindow === w ? 'tab-active' : ''"
            :aria-checked="state.filters.summaryWindow === w"
            @click="state.setFilter('summaryWindow', w)"
          >
            {{ w }}
          </button>
        </div>
      </fieldset>
      <fieldset class="min-w-0">
        <legend class="mb-1 text-xs font-medium text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.filters.traceWindow') }}</legend>
        <div class="tabs inline-flex" role="radiogroup" :aria-label="t('admin.cacheCharts.filters.traceWindow')">
          <button
            v-for="w in traceWindows"
            :key="w"
            type="button"
            role="radio"
            class="tab"
            :class="state.filters.traceWindow === w ? 'tab-active' : ''"
            :aria-checked="state.filters.traceWindow === w"
            @click="state.setFilter('traceWindow', w)"
          >
            {{ w }}
          </button>
        </div>
      </fieldset>
      <label class="block w-40">
        <span class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.filters.platform') }}</span>
        <select :value="state.filters.platform" class="input py-2" @change="state.setFilter('platform', ($event.target as HTMLSelectElement).value as PanelPlatform)">
          <option v-for="p in platforms" :key="p" :value="p">{{ t(`admin.cacheCharts.platforms.${p}`) }}</option>
        </select>
      </label>
      <label class="block w-44">
        <span class="mb-1 block text-xs font-medium text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.filters.costView') }}</span>
        <select :value="state.filters.costView" class="input py-2" data-test="cost-view" @change="state.setFilter('costView', ($event.target as HTMLSelectElement).value as CostView)">
          <option v-for="v in costViews" :key="v" :value="v">{{ t(`admin.cacheCharts.costViews.${v}`) }}</option>
        </select>
      </label>
      <div class="ml-auto flex flex-wrap items-center gap-3">
        <span v-if="state.source.value === 'mock'" class="badge badge-warning" data-test="synthetic-badge">{{ t('admin.cacheCharts.synthetic') }}</span>
        <span class="text-xs text-gray-600 dark:text-dark-300" data-test="freshness">
          <template v-if="asOf">{{ t('admin.cacheCharts.freshness', { time: formatAsOf(asOf), version: policyVersion }) }}</template>
        </span>
        <span v-if="stale" class="badge badge-warning" data-test="stale-badge">
          <Icon name="clock" size="xs" aria-hidden="true" />
          {{ t('admin.cacheCharts.stale') }}
        </span>
        <label class="flex items-center gap-2 text-xs text-gray-600 dark:text-dark-300">
          {{ t('admin.cacheCharts.filters.autoRefresh') }}
          <select :value="state.autoRefreshSeconds.value" class="input w-24 py-1.5 text-xs" @change="state.setAutoRefresh(Number(($event.target as HTMLSelectElement).value))">
            <option :value="0">{{ t('admin.cacheCharts.filters.autoOff') }}</option>
            <option :value="60">{{ t('admin.cacheCharts.filters.auto1m') }}</option>
            <option :value="300">{{ t('admin.cacheCharts.filters.auto5m') }}</option>
          </select>
        </label>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="refresh">
          <Icon name="refresh" size="sm" :class="busy ? 'animate-spin motion-reduce:animate-none' : ''" aria-hidden="true" />
          {{ t('admin.cacheCharts.filters.refresh') }}
        </button>
        <span class="sr-only" aria-live="polite">{{ announced }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { EMPTY, isStale } from '../viewModel'
import type { CacheChartsState } from '../useCacheCharts'
import type { CostView, PanelPlatform, SummaryWindow, TraceWindow } from '../types'

const props = defineProps<{ state: CacheChartsState; now: Date }>()
const { t } = useI18n()

const summaryWindows: SummaryWindow[] = ['24h', '7d', '30d']
const traceWindows: TraceWindow[] = ['1h', '24h']
const platforms: PanelPlatform[] = ['all', 'openai', 'anthropic', 'gemini', 'grok']
const costViews: CostView[] = ['list', 'charged', 'upstream']

// A failed read keeps the last answer for the next retry; freshness only speaks for a current one (R M2-U-3).
const current = computed(() => (props.state.northstar.status === 'ready' ? props.state.northstar.data : null))
const asOf = computed(() => current.value?.as_of ?? null)
const policyVersion = computed(() => current.value?.policy_version ?? EMPTY)
const stale = computed(() => isStale(asOf.value, props.now))
const busy = computed(() =>
  [props.state.northstar, props.state.breakdown, props.state.stats, props.state.experiments].some((s) => s.status === 'loading' || s.refreshing),
)
const announced = ref('')

function formatAsOf(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? EMPTY : `${d.toISOString().slice(0, 16).replace('T', ' ')} UTC`
}

async function refresh() {
  await props.state.loadAll()
  announced.value = t('admin.cacheCharts.updated')
}
</script>
