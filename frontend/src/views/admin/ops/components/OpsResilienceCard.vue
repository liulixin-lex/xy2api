<template>
  <section class="card p-4 md:p-5" data-testid="ops-resilience-card">
    <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('admin.resilience.panel.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.resilience.panel.hint') }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <div class="w-36">
          <Select v-model="windowValue" :options="windowOptions" :aria-label="t('admin.resilience.panel.window')" />
        </div>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="loadAll">{{ t('admin.resilience.panel.refresh') }}</button>
      </div>
    </div>

    <div v-if="statsError" role="alert" class="mb-4 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ statsError }}
    </div>

    <div class="mb-4">
      <p class="mb-2 text-xs font-semibold text-gray-500 dark:text-gray-400">{{ t('admin.resilience.panel.balanceStates') }}</p>
      <div class="flex flex-wrap gap-2" data-testid="resilience-balance-states">
        <span v-for="state in balanceStates" :key="state" :class="['rounded-full px-2.5 py-1 text-xs font-medium', balanceStateClass(state)]">
          {{ t(`admin.resilience.panel.states.${state}`) }} · {{ formatNumber(stats?.balance_states?.[state] ?? 0) }}
        </span>
      </div>
    </div>

    <div v-if="totals.length === 0 && !loading" class="py-4 text-center text-xs text-gray-500 dark:text-gray-400" data-testid="resilience-empty">
      {{ t('admin.resilience.panel.empty') }}
    </div>
    <template v-else>
      <div class="mb-4 grid grid-cols-2 gap-2 md:grid-cols-4" data-testid="resilience-totals">
        <div v-for="item in totals" :key="item.name" class="rounded-lg border border-gray-100 px-3 py-2 dark:border-dark-700">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ metricLabel(item.name) }}</p>
          <p class="text-lg font-semibold text-gray-900 dark:text-white">{{ formatNumber(item.count) }}</p>
        </div>
      </div>
      <div class="mb-6 max-h-72 overflow-auto rounded-xl border border-gray-200 dark:border-dark-700">
        <table class="min-w-full whitespace-nowrap text-left text-xs">
          <thead class="sticky top-0 bg-white dark:bg-dark-800">
            <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-gray-400">
              <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.panel.metric') }}</th>
              <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.panel.group') }}</th>
              <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.panel.account') }}</th>
              <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.panel.label') }}</th>
              <th class="px-2 py-2 text-right font-semibold">{{ t('admin.resilience.panel.count') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in counters" :key="rowKey(row)" class="border-b border-gray-100 text-gray-700 last:border-b-0 dark:border-dark-800 dark:text-gray-200">
              <td class="px-2 py-1.5">{{ metricLabel(row.name) }}</td>
              <td class="px-2 py-1.5">{{ row.group_id ? `#${row.group_id}` : '-' }}</td>
              <td class="px-2 py-1.5">{{ row.account_id ? `#${row.account_id}` : '-' }}</td>
              <td class="px-2 py-1.5">{{ row.label || '-' }}</td>
              <td class="px-2 py-1.5 text-right">{{ formatNumber(row.count) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
      <div>
        <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.resilience.undelivered.title') }}</h4>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.resilience.undelivered.hint') }}</p>
      </div>
      <div v-if="undeliveredPages > 1" class="flex items-center gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || page <= 1" @click="goPage(page - 1)">{{ t('admin.ops.openaiTokenStats.prevPage') }}</button>
        <span class="text-xs text-gray-500 dark:text-gray-400">{{ page }} / {{ undeliveredPages }}</span>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || page >= undeliveredPages" @click="goPage(page + 1)">{{ t('admin.ops.openaiTokenStats.nextPage') }}</button>
      </div>
    </div>
    <div v-if="undeliveredError" role="alert" class="mb-2 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ undeliveredError }}
    </div>
    <div v-else-if="undelivered.length === 0 && !loading" class="py-4 text-center text-xs text-gray-500 dark:text-gray-400" data-testid="resilience-undelivered-empty">
      {{ t('admin.resilience.undelivered.empty') }}
    </div>
    <div v-else class="max-h-96 overflow-auto rounded-xl border border-gray-200 dark:border-dark-700">
      <table class="min-w-full whitespace-nowrap text-left text-xs" data-testid="resilience-undelivered-table">
        <thead class="sticky top-0 bg-white dark:bg-dark-800">
          <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-gray-400">
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.time') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.stage') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.duration') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.user') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.apiKey') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.panel.group') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.model') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.endpoint') }}</th>
            <th class="px-2 py-2 text-right font-semibold">{{ t('admin.resilience.undelivered.cost') }}</th>
            <th class="px-2 py-2 font-semibold">{{ t('admin.resilience.undelivered.billed') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rec in undelivered" :key="rec.id" class="border-b border-gray-100 text-gray-700 last:border-b-0 dark:border-dark-800 dark:text-gray-200">
            <td class="px-2 py-1.5">{{ formatDateTime(rec.created_at) }}</td>
            <td class="px-2 py-1.5">{{ stageLabel(rec.disconnect_stage) }}</td>
            <td class="px-2 py-1.5">{{ formatSeconds(rec.duration_ms) }}</td>
            <td class="px-2 py-1.5">{{ rec.user_id ? `#${rec.user_id}` : '-' }}</td>
            <td class="px-2 py-1.5">{{ rec.api_key_id ? `#${rec.api_key_id}` : '-' }}</td>
            <td class="px-2 py-1.5">{{ rec.group_id ? `#${rec.group_id}` : '-' }}</td>
            <td class="px-2 py-1.5">{{ rec.model || '-' }}</td>
            <td class="px-2 py-1.5">{{ rec.inbound_endpoint || '-' }}</td>
            <td class="px-2 py-1.5 text-right">{{ rec.estimated_upstream_cost > 0 ? formatCostFixed(rec.estimated_upstream_cost, 6) : '-' }}</td>
            <td class="px-2 py-1.5">
              <span :class="rec.billed ? 'text-amber-600 dark:text-amber-400' : 'text-gray-500 dark:text-gray-400'">
                {{ rec.billed ? t('admin.resilience.undelivered.billed') : t('admin.resilience.undelivered.notBilled') }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <OpsResilienceExplain class="mt-6" />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import OpsResilienceExplain from './OpsResilienceExplain.vue'
import { adminAPI } from '@/api/admin'
import type { ResilienceMetricRow, ResilienceStats, UndeliveredRequest } from '@/api/admin/resilience'
import { formatCostFixed, formatDateTime, formatNumber } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ refreshToken: number }>()

const { t, te } = useI18n()

const balanceStates = ['OK', 'SUSPECT', 'EXHAUSTED', 'RECOVERING'] as const
const pageSize = 20
// The counters cover at most the last hour (in-process buckets).
const windowValue = ref('1h')
const windowOptions = computed(() => [
  { value: '5m', label: t('admin.ops.timeRange.5m') },
  { value: '30m', label: t('admin.ops.timeRange.30m') },
  { value: '1h', label: t('admin.ops.timeRange.1h') }
])

const loading = ref(false)
const stats = ref<ResilienceStats | null>(null)
const statsError = ref('')
const undelivered = ref<UndeliveredRequest[]>([])
const undeliveredPages = ref(1)
const undeliveredError = ref('')
const page = ref(1)

const counters = computed<ResilienceMetricRow[]>(() => stats.value?.counters ?? [])

const totals = computed(() => {
  const sum = new Map<string, number>()
  for (const row of counters.value) sum.set(row.name, (sum.get(row.name) ?? 0) + row.count)
  return [...sum.entries()].map(([name, count]) => ({ name, count }))
})

function rowKey(row: ResilienceMetricRow): string {
  return `${row.name}|${row.group_id ?? 0}|${row.account_id ?? 0}|${row.label ?? ''}`
}

function metricLabel(name: string): string {
  const key = `admin.resilience.panel.metrics.${name}`
  return te(key) ? t(key) : name
}

function stageLabel(stage: string): string {
  const key = `admin.resilience.undelivered.stages.${stage}`
  return stage && te(key) ? t(key) : stage || '-'
}

function balanceStateClass(state: string): string {
  switch (state) {
    case 'EXHAUSTED': return 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
    case 'SUSPECT': return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
    case 'RECOVERING': return 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-300'
    default: return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
  }
}

function formatSeconds(ms: number): string {
  return `${(ms / 1000).toFixed(1)}s`
}

async function loadStats() {
  statsError.value = ''
  try {
    stats.value = await adminAPI.resilience.stats(windowValue.value)
  } catch (cause) {
    statsError.value = extractApiErrorMessage(cause, t('admin.resilience.panel.loadFailed'))
  }
}

async function loadUndelivered() {
  undeliveredError.value = ''
  try {
    const result = await adminAPI.resilience.listUndelivered({ page: page.value, page_size: pageSize })
    undelivered.value = result.items ?? []
    undeliveredPages.value = Math.max(1, result.pages ?? 1)
  } catch (cause) {
    undeliveredError.value = extractApiErrorMessage(cause, t('admin.resilience.undelivered.loadFailed'))
  }
}

async function loadAll() {
  if (loading.value) return
  loading.value = true
  try {
    await Promise.all([loadStats(), loadUndelivered()])
  } finally {
    loading.value = false
  }
}

async function goPage(next: number) {
  page.value = next
  loading.value = true
  try {
    await loadUndelivered()
  } finally {
    loading.value = false
  }
}

watch(windowValue, () => { void loadStats() })
watch(() => props.refreshToken, () => { void loadAll() })
onMounted(loadAll)
</script>
