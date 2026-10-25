<template>
  <SectionShell
    id="routing"
    :title="t('admin.cacheCharts.routing.title')"
    :subtitle="t('admin.cacheCharts.trace.scope', { window: state.filters.traceWindow })"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    :skeleton-rows="7"
    @retry="state.loadStats()"
  >
    <TraceDisabled v-if="data && !data.trace_enabled" @open-settings="emit('open-settings')" />
    <template v-else-if="data">
      <div class="overflow-x-auto">
        <table class="w-full text-xs" data-test="route-reasons">
          <caption class="pb-2 text-left text-xs font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.routing.reasons') }}</caption>
          <thead>
            <tr class="text-left text-gray-600 dark:text-dark-300">
              <th scope="col" class="py-1.5 pr-3 font-medium">route_reason</th>
              <th scope="col" class="w-40 py-1.5 pr-3 font-medium">{{ t('admin.cacheCharts.routing.promptShare') }}</th>
              <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.requests') }}</th>
              <th scope="col" class="py-1.5 text-right font-medium">{{ t('admin.cacheCharts.columns.tokenHit') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 tabular-nums text-gray-800 dark:divide-dark-700 dark:text-gray-100">
            <tr v-for="r in reasons" :key="r.reason">
              <th scope="row" class="py-1.5 pr-3 text-left font-mono font-normal">{{ r.reason }}</th>
              <td class="py-1.5 pr-3">
                <span class="flex items-center gap-2">
                  <span class="h-1.5 rounded-sm bg-primary-600" :style="{ width: `${(r.share ?? 0) * 6}rem` }" aria-hidden="true" />
                  {{ formatPct(r.share, 1) }}
                </span>
              </td>
              <td class="py-1.5 pr-3 text-right">{{ formatCount(r.requests) }}</td>
              <td class="py-1.5 text-right">{{ formatPct(r.hit, 1) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <h3 class="mt-5 text-xs font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.routing.rebind') }}</h3>
      <p v-if="!rebinds.length" class="mt-1 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.routing.noRebind') }}</p>
      <ul v-else class="mt-2 flex flex-wrap gap-2" role="list">
        <li v-for="c in rebinds" :key="c.cause" class="rounded-md bg-gray-100 px-2 py-1 text-xs text-gray-800 dark:bg-dark-700 dark:text-gray-100">
          <code>{{ c.cause }}</code> <span class="tabular-nums">{{ formatCount(c.requests) }}</span>
        </li>
      </ul>

      <dl class="mt-5 flex items-baseline justify-between gap-4 border-t border-gray-100 pt-4 text-sm dark:border-dark-700">
        <dt class="text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.routing.switchRate') }}</dt>
        <dd class="tabular-nums text-gray-900 dark:text-gray-100">{{ formatPct(data.model_isolated_switch_rate) }}</dd>
      </dl>
    </template>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CacheChartsState } from '../useCacheCharts'
import { formatCount, formatPct } from '../viewModel'
import SectionShell from './SectionShell.vue'
import TraceDisabled from './TraceDisabled.vue'

const props = defineProps<{ state: CacheChartsState }>()
const emit = defineEmits<{ (e: 'open-settings'): void }>()
const { t } = useI18n()

const section = computed(() => props.state.stats)
const data = computed(() => section.value.data)
const reasons = computed(() => {
  const list = data.value?.route_reasons ?? []
  const total = list.reduce((s, r) => s + r.prompt_tokens, 0)
  return [...list]
    .sort((a, b) => b.prompt_tokens - a.prompt_tokens)
    .map((r) => ({ reason: r.reason, requests: r.requests, share: total ? r.prompt_tokens / total : null, hit: r.prompt_tokens ? r.read_tokens / r.prompt_tokens : null }))
})
const rebinds = computed(() => [...(data.value?.rebind_causes ?? [])].filter((c) => c.requests > 0).sort((a, b) => b.requests - a.requests))
</script>
