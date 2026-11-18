<template>
  <div :class="['grid grid-cols-2 gap-4', e2eShown ? 'lg:grid-cols-5' : 'lg:grid-cols-4']">
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-blue-100 p-2 dark:bg-blue-900/30 text-blue-600">
        <Icon name="document" size="md" />
      </div>
      <div>
        <p class="text-xs font-medium text-gray-500">{{ t('usage.totalRequests') }}</p>
        <p class="text-xl font-bold">{{ stats?.total_requests?.toLocaleString() || '0' }}</p>
        <!-- 管理端（后端 0.6.5 请求层）带结果拆分；用户端没有这些字段，仍显示范围说明 -->
        <p v-if="outcomes" class="text-xs text-gray-500" data-testid="usage-request-outcomes">
          {{ t('admin.usage.requestOutcomes', outcomes) }}
        </p>
        <p v-else class="text-xs text-gray-400">{{ t('usage.inSelectedRange') }}</p>
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-amber-100 p-2 dark:bg-amber-900/30 text-amber-600"><svg class="h-5 w-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9" /></svg></div>
      <div>
        <p class="text-xs font-medium text-gray-500">{{ t('usage.totalTokens') }}</p>
        <p class="text-xl font-bold">{{ formatTokens(stats?.total_tokens || 0) }}</p>
        <p class="flex flex-wrap items-center gap-x-1 text-xs text-gray-500">
          <span>{{ t('usage.in') }}: {{ formatTokens(stats?.total_input_tokens || 0) }}</span>
          <span>/</span>
          <span>{{ t('usage.out') }}: {{ formatTokens(stats?.total_output_tokens || 0) }}</span>
          <span>/</span>
          <span class="group relative inline-flex cursor-help items-center gap-0.5" tabindex="0">
            <span>{{ cacheLabel() }}: {{ formatTokens(stats?.total_cache_tokens || 0) }}</span>
            <svg
              class="h-3.5 w-3.5 text-gray-400"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
              />
            </svg>
            <span
              class="pointer-events-none absolute left-1/2 top-full z-30 mt-2 hidden w-56 -translate-x-1/2 rounded-lg border border-gray-200 bg-white p-3 text-left text-xs text-gray-700 shadow-lg group-hover:block group-focus:block dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200"
            >
              <span class="mb-2 block font-medium text-gray-900 dark:text-white">
                {{ cacheDetailLabel() }}
              </span>
              <span class="flex items-center justify-between gap-3">
                <span>{{ t('usage.cacheCreationTokensLabel') }}</span>
                <span class="tabular-nums">
                  {{ formatTokens(stats?.total_cache_creation_tokens || 0) }}
                </span>
              </span>
              <span class="mt-1 flex items-center justify-between gap-3">
                <span>{{ t('usage.cacheReadTokensLabel') }}</span>
                <span class="tabular-nums">
                  {{ formatTokens(stats?.total_cache_read_tokens || 0) }}
                </span>
              </span>
            </span>
          </span>
        </p>
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-green-100 p-2 dark:bg-green-900/30 text-green-600">
        <Icon name="dollar" size="md" />
      </div>
      <div class="min-w-0 flex-1">
        <p class="text-xs font-medium text-gray-500">{{ t('usage.totalCost') }}</p>
        <p class="text-xl font-bold text-green-600">
          ${{ (stats?.total_actual_cost || 0).toFixed(4) }}
        </p>
        <p class="text-xs text-gray-400">
          <template v-if="showAccountCost && totalAccountCost != null">
            <span class="text-orange-500">{{ t('usage.accountCost') }} ${{ totalAccountCost.toFixed(4) }}</span>
            <span> · </span>
          </template>
          <span>
            {{ t('usage.standardCost') }}
            <span :class="{ 'line-through': strikeStandardCost }">${{ (stats?.total_cost || 0).toFixed(4) }}</span>
          </span>
        </p>
      </div>
    </div>
    <div class="card p-4 flex items-center gap-3">
      <div class="rounded-lg bg-purple-100 p-2 dark:bg-purple-900/30 text-purple-600">
        <Icon name="clock" size="md" />
      </div>
      <div><p class="text-xs font-medium text-gray-500">{{ t('usage.avgDuration') }}</p><p class="text-xl font-bold">{{ formatDuration(stats?.average_duration_ms || 0) }}</p></div>
    </div>
    <!-- 管理端：端到端首输出的中位数（用户实际等了多久）和样本数；用户端接口没有这些字段，页面不传 e2eState，不显示 -->
    <div v-if="e2eShown" class="card p-4 flex items-center gap-3" data-testid="usage-e2e-card" :title="t('usage.e2eFirstOutputHint')">
      <div class="rounded-lg bg-sky-100 p-2 dark:bg-sky-900/30 text-sky-600">
        <Icon name="clock" size="md" />
      </div>
      <div>
        <p class="text-xs font-medium text-gray-500">{{ t('usage.e2eFirstOutputP50') }}</p>
        <p class="text-xl font-bold" data-testid="usage-e2e-p50">{{ e2e.value }}</p>
        <p class="text-xs text-gray-400" data-testid="usage-e2e-samples">{{ e2e.note }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import type { UsageStatsResponse } from '@/types'
import Icon from '@/components/icons/Icon.vue'

const props = withDefaults(defineProps<{
  stats: (AdminUsageStatsResponse | UsageStatsResponse) | null
  showAccountCost?: boolean
  strikeStandardCost?: boolean
  /**
   * Admin page only: whether `stats` answer the current filter (loaded), are still on their way, failed to load, or
   * were not read because the list failed.
   * Without it there is no user-wait card (the user page's stats have no end-to-end fields).
   */
  e2eState?: 'loading' | 'failed' | 'loaded' | 'blocked'
}>(), {
  showAccountCost: true,
  strikeStandardCost: false,
})

const { t } = useI18n()

const totalAccountCost = computed(() => {
  const stats = props.stats as (AdminUsageStatsResponse & { total_account_cost?: number }) | null
  return stats?.total_account_cost ?? null
})
// Success / rescued / failed counts of the admin request stats; absent on the user page and older backends.
const outcomes = computed(() => {
  const stats = props.stats as (AdminUsageStatsResponse | null)
  if (!stats || typeof stats.success !== 'number') return null
  return {
    success: stats.success.toLocaleString(),
    rescued: (stats.rescued ?? 0).toLocaleString(),
    failed: (stats.failed ?? 0).toLocaleString()
  }
})
// The median user wait (end-to-end first output) of the current filter: its value with the requests it is over, or
// why there is none (still loading, failed to load, an answer without the figures, no records in the range).
const e2eShown = computed(() => props.e2eState !== undefined)
const e2e = computed(() => {
  const unknown = t('usage.latencyUnknown')
  if (props.e2eState === 'loading') return { value: '…', note: t('usage.e2eFirstOutputLoading') }
  if (props.e2eState === 'failed') return { value: unknown, note: t('usage.e2eFirstOutputFailed') }
  if (props.e2eState === 'blocked') return { value: unknown, note: t('usage.e2eFirstOutputBlocked') }
  const stats = props.stats as (AdminUsageStatsResponse | null)
  const samples = stats?.e2e_first_output_samples
  if (typeof samples !== 'number') return { value: unknown, note: t('usage.e2eFirstOutputMissing') }
  const p50 = stats?.e2e_first_output_p50_ms
  if (samples <= 0) return { value: unknown, note: t('usage.e2eFirstOutputNone') }
  // Samples counted, but no median in the answer: the count stands, the median is unknown.
  if (p50 == null) return { value: unknown, note: t('usage.e2eFirstOutputP50Missing', { n: samples.toLocaleString() }) }
  return { value: formatDuration(p50), note: t('usage.e2eFirstOutputSamples', { n: samples.toLocaleString() }) }
})
const showAccountCost = computed(() => props.showAccountCost)
const strikeStandardCost = computed(() => props.strikeStandardCost)

const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms.toFixed(0)}ms` : `${(ms / 1000).toFixed(2)}s`

const formatTokens = (value: number) => {
  if (value >= 1e9) return (value / 1e9).toFixed(2) + 'B'
  if (value >= 1e6) return (value / 1e6).toFixed(2) + 'M'
  if (value >= 1e3) return (value / 1e3).toFixed(2) + 'K'
  return value.toLocaleString()
}

const cacheLabel = () => t('usage.cacheTotal')
const cacheDetailLabel = () => t('usage.cacheBreakdown')
</script>
