<template>
  <!-- 状态在上，下面依次是近期错误、首字、缓存率三行近期健康数据，各自带文字标签 -->
  <div class="flex flex-col items-start gap-1">
    <slot />
    <a
      v-if="recentErrors > 0"
      :href="errorsHref"
      target="_blank"
      rel="noopener"
      data-test="recent-errors"
      class="text-[11px] font-medium leading-4 tabular-nums hover:underline"
      :class="recentErrors >= RECENT_ERRORS_DANGER ? 'text-red-600 dark:text-red-400' : 'text-amber-600 dark:text-amber-400'"
      :title="t('admin.accounts.recentErrorsTitle', { count: recentErrors })"
    >{{ t('admin.accounts.recentErrors', { count: recentErrors }) }}</a>
    <span
      v-if="firstTokenAvgMs != null"
      data-test="first-token-avg"
      class="text-[11px] leading-4 tabular-nums"
      :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(firstTokenAvgMs)]"
      :title="t('admin.accounts.firstTokenAvgTitle', { count: health?.first_token_samples ?? 0 })"
    >{{ t('admin.accounts.firstTokenAvg', { value: formatFirstToken(firstTokenAvgMs) }) }}</span>
    <span
      v-if="cacheRatio != null"
      data-test="cache-read-ratio"
      class="text-[11px] leading-4 tabular-nums text-gray-500 dark:text-gray-400"
      :title="t('admin.accounts.cacheReadRatioTitle', { count: health?.cache_samples ?? 0 })"
    >{{ t('admin.accounts.cacheReadRatio', { value: cacheRatio.toFixed(1) + '%' }) }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountRecentHealth } from '@/api/admin/accounts'
import { LATENCY_TEXT_CLASSES, firstTokenSeverity } from '@/utils/latencyHealth'
import { cacheReadRatio } from '@/utils/usageMetrics'

const props = defineProps<{
  accountId: number
  health?: AccountRecentHealth | null
}>()

const { t } = useI18n()

// 近 10 分钟的错误请求数：1–4 次黄色，5 次及以上红色；0 不显示。
const RECENT_ERRORS_DANGER = 5
const recentErrors = computed(() => props.health?.recent_errors ?? 0)
const firstTokenAvgMs = computed(() => props.health?.first_token_avg_ms ?? null)
// 近 30 分钟三项 Token 先求和再算比例，口径同使用记录的“缓存读取占比”；三项之和为 0 时为 null，不显示。
// 这是参考数据而非状态，用中性灰；状态色只留给错误数和首字延迟分档。
const cacheRatio = computed(() => (props.health ? cacheReadRatio(props.health) : null))

// 现有的“使用记录 → 错误请求”页，UsageView 读取 tab 与 account_id 并筛选该账号。
// 与 createWebHistory(BASE_URL) 生成的地址一致，不依赖注入的 router。
const errorsHref = computed(() => {
  const query = new URLSearchParams({ tab: 'errors', account_id: String(props.accountId) })
  return `${import.meta.env.BASE_URL.replace(/\/+$/, '')}/admin/usage?${query}`
})

// 与使用记录“延迟”列的格式一致。
const formatFirstToken = (ms: number): string => {
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const totalSec = Math.round(ms / 1000)
  return `${Math.floor(totalSec / 60)}m ${totalSec % 60}s`
}
</script>
