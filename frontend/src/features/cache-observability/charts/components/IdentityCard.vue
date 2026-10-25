<template>
  <SectionShell
    id="identity"
    :title="t('admin.cacheCharts.identity.title')"
    :subtitle="t('admin.cacheCharts.trace.scope', { window: state.filters.traceWindow })"
    :status="section.status"
    :refreshing="section.refreshing"
    :error="section.error"
    :skeleton-rows="7"
    @retry="state.loadStats()"
  >
    <template #actions><ScopeTags :tags="tags" /></template>
    <TraceDisabled v-if="data && !data.trace_enabled" @open-settings="emit('open-settings')" />
    <p v-else-if="data && scope?.openaiMetricsAbsent" class="text-sm text-gray-600 dark:text-dark-300" data-test="identity-openai-only">
      {{ t('admin.cacheCharts.identity.openaiOnlyEmpty') }}
    </p>
    <template v-else-if="data">
      <h3 class="text-xs font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.identity.pckOut') }}</h3>
      <div class="mt-2 overflow-x-auto">
        <table class="w-full text-xs" data-test="pck-out">
          <thead>
            <tr class="text-left text-gray-600 dark:text-dark-300">
              <th scope="col" class="py-1.5 pr-3 font-medium">pck_class_out</th>
              <th scope="col" class="py-1.5 pr-3 font-medium">relay_family</th>
              <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.identity.promptShare') }}</th>
              <th scope="col" class="py-1.5 pr-3 text-right font-medium">{{ t('admin.cacheCharts.columns.tokenHit') }}</th>
              <th scope="col" class="py-1.5 text-right font-medium">{{ t('admin.cacheCharts.identity.underRead') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 tabular-nums text-gray-800 dark:divide-dark-700 dark:text-gray-100">
            <tr v-for="row in pckOut" :key="`${row.class}-${row.relay_family}`">
              <th scope="row" class="py-1.5 pr-3 text-left font-mono font-normal">{{ row.class }}</th>
              <td class="py-1.5 pr-3 font-mono">{{ row.relay_family }}</td>
              <td class="py-1.5 pr-3 text-right">{{ formatPct(share(row.prompt_tokens, outPrompt), 1) }}</td>
              <td class="py-1.5 pr-3 text-right">{{ formatPct(share(row.read_tokens, row.prompt_tokens), 1) }}</td>
              <td class="py-1.5 text-right">{{ formatPct(share(row.under_read_tokens, row.prompt_tokens), 1) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-if="allRelayUnknown" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('admin.cacheCharts.identity.relayPending') }}</p>
      <p v-if="typeof data.pck_out_is_uuid_ratio === 'number'" class="mt-1 text-xs text-gray-600 dark:text-dark-300">{{ t('admin.cacheCharts.identity.uuidShare', { share: formatPct(data.pck_out_is_uuid_ratio, 1) }) }}</p>

      <h3 class="mt-5 text-xs font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.identity.pckIn') }}</h3>
      <ul class="mt-2 space-y-1.5" role="list">
        <li v-for="row in pckIn" :key="row.class" class="grid grid-cols-[9rem_minmax(0,1fr)_3.5rem] items-center gap-3 text-xs">
          <code class="truncate text-gray-700 dark:text-dark-200">{{ row.class }}</code>
          <span class="h-2 rounded-sm bg-primary-600" :style="{ width: `${(share(row.prompt_tokens, inPrompt) ?? 0) * 100}%` }" aria-hidden="true" />
          <span class="text-right tabular-nums text-gray-800 dark:text-gray-100">{{ formatPct(share(row.prompt_tokens, inPrompt), 1) }}</span>
        </li>
      </ul>

      <h3 class="mt-5 text-xs font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.identity.headers') }}</h3>
      <p class="mt-1 flex gap-4 text-xs text-gray-600 dark:text-dark-300">
        <span class="flex items-center gap-1.5"><span class="h-2 w-3 rounded-sm" :style="{ backgroundColor: inColor }" aria-hidden="true" />{{ t('admin.cacheCharts.identity.inbound') }}</span>
        <span class="flex items-center gap-1.5"><span class="h-2 w-3 rounded-sm" :style="{ backgroundColor: outColor }" aria-hidden="true" />{{ t('admin.cacheCharts.identity.outbound') }}</span>
      </p>
      <ul class="mt-2 space-y-2" role="list" data-test="session-headers">
        <li v-for="h in data.session_headers" :key="h.header" class="grid grid-cols-[9rem_minmax(0,1fr)_6.5rem] items-center gap-3 text-xs">
          <code class="truncate text-gray-700 dark:text-dark-200">{{ h.header }}</code>
          <span class="space-y-0.5" aria-hidden="true">
            <span class="block h-1.5 rounded-sm" :style="{ width: `${h.in_ratio * 100}%`, backgroundColor: inColor }" />
            <span class="block h-1.5 rounded-sm" :style="{ width: `${h.out_ratio * 100}%`, backgroundColor: outColor }" />
          </span>
          <span class="text-right tabular-nums text-gray-800 dark:text-gray-100">{{ formatPct(h.in_ratio, 0) }} / {{ formatPct(h.out_ratio, 0) }}</span>
        </li>
      </ul>

      <p v-if="data.sess_out_relation.length" class="mt-4 text-xs text-gray-600 dark:text-dark-300">
        <span class="font-semibold text-gray-700 dark:text-dark-200">{{ t('admin.cacheCharts.identity.relation') }}</span>
        <template v-for="(r, i) in relations" :key="r.relation"><span v-if="i">, </span><code>{{ r.relation }}</code> {{ formatPct(r.share, 1) }}</template>
      </p>
    </template>
  </SectionShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CacheChartsState } from '../useCacheCharts'
import { formatPct, LOSS_COLORS, statsScope } from '../viewModel'
import ScopeTags from './ScopeTags.vue'
import SectionShell from './SectionShell.vue'
import TraceDisabled from './TraceDisabled.vue'

const props = defineProps<{ state: CacheChartsState }>()
const emit = defineEmits<{ (e: 'open-settings'): void }>()
const { t } = useI18n()

const section = computed(() => props.state.stats)
const data = computed(() => section.value.data)
const scope = computed(() => (data.value ? statsScope(data.value) : null))
const tags = computed(() => {
  const list: ('openaiOnly' | 'bridgeOnly')[] = []
  if (scope.value?.openaiOnly) list.push('openaiOnly')
  if (scope.value?.bridgeOnly) list.push('bridgeOnly')
  return list
})
const dark = computed(() => document.documentElement.classList.contains('dark'))
// Two series from the validated categorical slots 1 and 2.
const inColor = computed(() => (dark.value ? LOSS_COLORS.prefix_changed.dark : LOSS_COLORS.prefix_changed.light))
const outColor = computed(() => (dark.value ? LOSS_COLORS.under_read.dark : LOSS_COLORS.under_read.light))

function share(part: number, whole: number): number | null {
  return whole ? part / whole : null
}
const pckOut = computed(() => [...(data.value?.pck_out ?? [])].sort((a, b) => b.prompt_tokens - a.prompt_tokens))
const outPrompt = computed(() => pckOut.value.reduce((s, r) => s + r.prompt_tokens, 0))
const pckIn = computed(() => [...(data.value?.pck_in ?? [])].sort((a, b) => b.prompt_tokens - a.prompt_tokens))
const inPrompt = computed(() => pckIn.value.reduce((s, r) => s + r.prompt_tokens, 0))
const allRelayUnknown = computed(() => pckOut.value.length > 0 && pckOut.value.every((r) => r.relay_family === 'unknown'))
const relations = computed(() => {
  const list = data.value?.sess_out_relation ?? []
  const total = list.reduce((s, r) => s + r.prompt_tokens, 0)
  return [...list].sort((a, b) => b.prompt_tokens - a.prompt_tokens).map((r) => ({ relation: r.relation, share: share(r.prompt_tokens, total) }))
})
</script>
