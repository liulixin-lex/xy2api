<template>
  <div class="flex h-6 min-w-[5rem] items-center">
    <HelpTooltip
      v-if="item"
      class="-ml-1"
      width-class="w-max max-w-[calc(100vw-2rem)]"
      data-testid="upstream-balance-details"
    >
      <template #trigger>
        <span
          class="cursor-help border-b border-dotted border-gray-300 text-sm font-medium dark:border-dark-600"
          :class="valueClass"
          data-testid="upstream-balance-value"
        >
          {{ displayValue }}
        </span>
      </template>
      <div class="space-y-1">
        <p data-testid="upstream-balance-basis">
          {{ t('admin.accounts.upstreamBalance.basisLine', { value: basisLabel }) }}
        </p>
        <p data-testid="upstream-balance-reason">
          {{ t('admin.accounts.upstreamBalance.reasonLine', { value: reasonLabel }) }}
        </p>
        <p data-testid="upstream-balance-updated">{{ updatedLabel }}</p>
      </div>
    </HelpTooltip>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500" data-testid="upstream-balance-value">—</span>
  </div>
</template>

<script setup lang="ts">
// 上游余额列单元格（xy-upstream-balance）：只展示父组件传入的缓存值，不发请求、不带定时器。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import type { UpstreamBalanceItem } from '@/api/admin/accounts'

const props = defineProps<{
  item?: UpstreamBalanceItem
  /** 已按 server_time 校正过时钟偏差的当前时间（毫秒） */
  now: number
}>()

const { t } = useI18n()

// 后端最近一次尝试失败且已超过 3 分钟没有成功，或无论如何超过 10 分钟没有成功：变灰。
const STALE_AFTER_FAILED_ATTEMPT_MS = 3 * 60 * 1000
const STALE_AFTER_MS = 10 * 60 * 1000

const usdFormatter = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 2
})

// 先四舍五入到分再格式化，-0.004 之类显示 $0.00 而不是 -$0.00；真实的负数（上游欠费）原样显示。
function formatUsd(value: number): string {
  const cents = Math.round(value * 100) / 100
  return usdFormatter.format(cents === 0 ? 0 : cents)
}

function parseTime(value: string | null | undefined): number {
  return typeof value === 'string' ? Date.parse(value) : Number.NaN
}

const hasValue = computed(() => {
  const item = props.item
  if (!item || item.state !== 'ok') return false
  return item.unlimited || (typeof item.amount_usd === 'number' && Number.isFinite(item.amount_usd))
})

const observedMs = computed(() => parseTime(props.item?.observed_at))

const stale = computed(() => {
  if (!hasValue.value) return false
  const observed = observedMs.value
  if (!Number.isFinite(observed)) return true
  const age = props.now - observed
  if (age > STALE_AFTER_MS) return true
  const lastAttempt = parseTime(props.item?.last_attempt_at)
  return age > STALE_AFTER_FAILED_ATTEMPT_MS && Number.isFinite(lastAttempt) && lastAttempt > observed
})

const displayValue = computed(() => {
  const item = props.item
  if (!item || !hasValue.value) return '—'
  if (item.unlimited) return '∞'
  return formatUsd(item.amount_usd as number)
})

const valueClass = computed(() => {
  if (!hasValue.value) return 'text-gray-400 dark:text-gray-500'
  return stale.value ? 'font-mono text-gray-400 dark:text-gray-500' : 'font-mono text-gray-800 dark:text-gray-200'
})

const basisLabel = computed(() => {
  switch (props.item?.basis) {
    case 'wallet':
      return t('admin.accounts.upstreamBalance.basis.wallet')
    case 'key':
      return t('admin.accounts.upstreamBalance.basis.key')
    case 'subscription':
      return t('admin.accounts.upstreamBalance.basis.subscription')
    default:
      return t('admin.accounts.upstreamBalance.basis.unknown')
  }
})

const reasonLabel = computed(() => {
  const item = props.item
  if (!item) return ''
  const reason = item.reason as string | null
  if (reason == null) {
    if (item.state === 'not_applicable') return t('admin.accounts.upstreamBalance.reason.notApplicable')
    if (item.state === 'ok') {
      return stale.value ? t('admin.accounts.upstreamBalance.reason.stale') : t('admin.accounts.upstreamBalance.reason.none')
    }
    return t('admin.accounts.upstreamBalance.reason.pending')
  }
  switch (reason) {
    case 'unidentified':
      return t('admin.accounts.upstreamBalance.reason.unidentified')
    case 'rate_limited':
      return t('admin.accounts.upstreamBalance.reason.rateLimited')
    case 'auth_failed':
      return t('admin.accounts.upstreamBalance.reason.authFailed')
    case 'token_invalid':
      return t('admin.accounts.upstreamBalance.reason.tokenInvalid')
    case 'key_ungrouped':
      return t('admin.accounts.upstreamBalance.reason.keyUngrouped')
    case 'key_exhausted':
      return t('admin.accounts.upstreamBalance.reason.keyExhausted')
    case 'upstream_error':
      return t('admin.accounts.upstreamBalance.reason.upstreamError')
    case 'no_data':
      return t('admin.accounts.upstreamBalance.reason.noData')
    case 'redis_unavailable':
      return t('admin.accounts.upstreamBalance.reason.redisUnavailable')
    default:
      return t('admin.accounts.upstreamBalance.reason.unknown')
  }
})

const updatedLabel = computed(() => {
  const observed = observedMs.value
  if (!Number.isFinite(observed)) return t('admin.accounts.upstreamBalance.neverUpdated')
  const seconds = Math.max(0, Math.floor((props.now - observed) / 1000))
  if (seconds < 60) return t('admin.accounts.upstreamBalance.updatedSecondsAgo', { count: seconds })
  if (seconds < 3600) return t('admin.accounts.upstreamBalance.updatedMinutesAgo', { count: Math.floor(seconds / 60) })
  if (seconds < 86400) return t('admin.accounts.upstreamBalance.updatedHoursAgo', { count: Math.floor(seconds / 3600) })
  return t('admin.accounts.upstreamBalance.updatedDaysAgo', { count: Math.floor(seconds / 86400) })
})
</script>
