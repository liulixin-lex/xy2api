<template>
  <div class="flex flex-col items-start gap-1">
    <div class="flex items-center gap-1.5">
      <slot />
      <a
        v-if="recentErrors > 0"
        :href="errorsHref"
        target="_blank"
        rel="noopener"
        data-test="recent-errors"
        class="rounded px-1 text-[11px] font-semibold leading-4 tabular-nums text-red-600 transition-colors hover:bg-red-50 hover:underline dark:text-red-400 dark:hover:bg-red-900/20"
        :title="t('admin.accounts.recentErrorsTitle', { count: recentErrors })"
      >{{ recentErrors }}</a>
    </div>
    <span
      v-if="firstTokenAvgMs != null"
      data-test="first-token-avg"
      class="text-[11px] leading-4 tabular-nums"
      :class="LATENCY_TEXT_CLASSES[firstTokenSeverity(firstTokenAvgMs)]"
      :title="t('admin.accounts.firstTokenAvgTitle', { count: health?.first_token_samples ?? 0 })"
    >{{ t('admin.accounts.firstTokenAvg', { value: formatFirstToken(firstTokenAvgMs) }) }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountRecentHealth } from '@/api/admin/accounts'
import { LATENCY_TEXT_CLASSES, firstTokenSeverity } from '@/utils/latencyHealth'

const props = defineProps<{
  accountId: number
  health?: AccountRecentHealth | null
}>()

const { t } = useI18n()

const recentErrors = computed(() => props.health?.recent_errors ?? 0)
const firstTokenAvgMs = computed(() => props.health?.first_token_avg_ms ?? null)

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
