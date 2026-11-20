<template>
  <div class="space-y-6">
    <!-- 电脑端不变：统计卡，日期范围和粒度，图表 -->
    <template v-if="wide">
      <UsageStatsCards v-bind="cardProps" />
      <div class="space-y-4">
        <div class="card p-4">
          <div class="flex flex-wrap items-center gap-4">
            <slot name="range" />
            <div class="ml-auto"><slot name="granularity" /></div>
          </div>
        </div>
        <slot name="charts" />
      </div>
    </template>

    <!--
      1024 以下默认收起：日期范围（它同时控制下面的列表，所以留在收起区外）、一行摘要和“展开图表”。
      收起时统计卡和图表都不挂载；展开后按当前筛选挂载。
    -->
    <template v-else>
      <div class="card space-y-3 p-4" data-testid="usage-overview-compact">
        <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
          <slot name="range" />
          <button
            type="button"
            data-testid="usage-overview-toggle"
            class="btn btn-secondary btn-sm [@media(pointer:coarse)]:min-h-10"
            :aria-expanded="expanded"
            :aria-controls="panelId"
            @click="expanded = !expanded"
          >
            {{ expanded ? t('usage.overview.collapse') : t('usage.overview.expand') }}
            <Icon name="chevronDown" size="sm" class="transition-transform duration-200" :class="{ 'rotate-180': expanded }" />
          </button>
        </div>
        <!-- 每一项“名称 数值”不拆开，放不下时整项换行（项间用间距分开，换行时不留悬空的分隔点） -->
        <p class="flex flex-wrap items-baseline gap-x-4 gap-y-1 text-sm text-gray-500 dark:text-gray-400" data-testid="usage-overview-summary">
          <span class="whitespace-nowrap">{{ t('usage.overview.requests') }} <span class="font-medium tabular-nums text-gray-900 dark:text-white">{{ summary.requests }}</span></span>
          <span class="whitespace-nowrap">{{ t('usage.overview.tokens') }} <span class="font-medium tabular-nums text-gray-900 dark:text-white">{{ summary.tokens }}</span></span>
          <span class="whitespace-nowrap">{{ t('usage.overview.cost') }} <span class="font-medium tabular-nums text-green-600 dark:text-green-400">{{ summary.cost }}</span></span>
        </p>
      </div>
      <div v-if="expanded" :id="panelId" class="space-y-4" data-testid="usage-overview-charts">
        <UsageStatsCards v-bind="cardProps" />
        <div class="flex justify-end"><slot name="granularity" /></div>
        <slot name="charts" />
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMediaQuery } from '@vueuse/core'
import UsageStatsCards from './UsageStatsCards.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatUsdTotal } from '@/utils/format'
import { formatTokenTotal } from './usageRow'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import type { UsageStatsResponse } from '@/types'

/**
 * The usage page's stats cards, date range and charts (admin and user page alike). From 1024px as before; below it the
 * date range and a one-line summary stay, and the cards and charts mount only when expanded (collapsed on each visit).
 */
const props = withDefaults(defineProps<{
  stats: (AdminUsageStatsResponse | UsageStatsResponse) | null
  showAccountCost?: boolean
  strikeStandardCost?: boolean
  /** Admin page only: whether `stats` answer the current filter (see UsageStatsCards). */
  e2eState?: 'loading' | 'failed' | 'loaded' | 'blocked'
}>(), {
  showAccountCost: true,
  strikeStandardCost: false,
})

const { t } = useI18n()
const wide = useMediaQuery('(min-width: 1024px)')
const expanded = ref(false)
const panelId = `usage-overview-${useId()}`

const cardProps = computed(() => ({
  stats: props.stats,
  showAccountCost: props.showAccountCost,
  strikeStandardCost: props.strikeStandardCost,
  ...(props.e2eState !== undefined ? { e2eState: props.e2eState } : {}),
}))

/** The same totals as the cards; on the admin page "…" while loading and unknown on failure, as the cards show. */
const summary = computed(() => {
  if (props.e2eState !== undefined && props.e2eState !== 'loaded') {
    const unread = props.e2eState === 'loading' ? '…' : t('usage.latencyUnknown')
    return { requests: unread, tokens: unread, cost: unread }
  }
  return {
    requests: (props.stats?.total_requests ?? 0).toLocaleString(),
    tokens: formatTokenTotal(props.stats?.total_tokens ?? 0),
    cost: formatUsdTotal(props.stats?.total_actual_cost),
  }
})
</script>
