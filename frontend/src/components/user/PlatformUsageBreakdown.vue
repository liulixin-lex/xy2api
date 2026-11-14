<template>
  <div class="text-sm">
    <div class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.users.today') }}:</span>
      <span class="font-medium text-gray-900 dark:text-white" v-tip="exactMoneyTip(today)">{{ formatCompactMoney(today) }}</span>
      <!-- The split by platform is the app's shared tip, in the body and kept on screen: it takes no room while hidden,
           shows on hover or keyboard focus, and a tap shows it at once (7r-3). A 24px target over the 12px icon. -->
      <button
        v-if="hasBreakdown"
        type="button"
        class="-m-1.5 inline-flex rounded p-1.5 text-gray-400 hover:text-gray-600 dark:text-gray-500 dark:hover:text-gray-300"
        :aria-label="t('admin.users.platformBreakdown')"
        v-tip="breakdownTip"
        data-test="platform-breakdown"
        @click="showTipNow($event.currentTarget)"
      >
        <Icon name="infoCircle" size="xs" />
      </button>
    </div>
    <div class="mt-0.5 flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.users.total') }}:</span>
      <span class="font-medium text-gray-900 dark:text-white" v-tip="exactMoneyTip(total)">{{ formatCompactMoney(total) }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { exactMoneyTip, formatCompactMoney } from '@/utils/compactNumber'
import { showTipNow, vTip } from '@/utils/tip'
import type { PlatformUsage } from '@/api/admin/dashboard'

const props = defineProps<{
  today: number
  total: number
  byPlatform?: PlatformUsage[]
}>()

const { t } = useI18n()

// 与 UserDashboardStats 保持一致：把"总值 - 各平台之和"的差作为"其他"行展示，
// 避免 tooltip 内各平台费用加总与列首总值对不上。
const OTHER_THRESHOLD = 0.0001

interface BreakdownRow {
  platform: string
  today_actual_cost: number
  total_actual_cost: number
  isOther?: boolean
}

const sortedBreakdown = computed<BreakdownRow[]>(() => {
  const list = props.byPlatform ?? []
  const rows: BreakdownRow[] = [...list]
    .sort((a, b) => b.total_actual_cost - a.total_actual_cost)
    .map((p) => ({ ...p }))

  const sumTotal = rows.reduce((s, r) => s + r.total_actual_cost, 0)
  const sumToday = rows.reduce((s, r) => s + r.today_actual_cost, 0)
  const diffTotal = Math.max(0, props.total - sumTotal)
  const diffToday = Math.max(0, props.today - sumToday)
  if (diffTotal > OTHER_THRESHOLD || diffToday > OTHER_THRESHOLD) {
    rows.push({
      platform: '__other__',
      today_actual_cost: diffToday,
      total_actual_cost: diffTotal,
      isOther: true
    })
  }
  return rows
})

const hasBreakdown = computed(() => sortedBreakdown.value.length > 0)

/** One line per platform, today / total in full (the tip keeps line breaks and spaces). */
const breakdownTip = computed(() =>
  [
    `${t('admin.users.platformBreakdown')}  ${t('admin.users.today')} / ${t('admin.users.total')}`,
    ...sortedBreakdown.value.map((item) => {
      const name = item.isOther ? t('admin.users.platformOther') : platformLabel(item.platform)
      return `${name}  $${item.today_actual_cost.toFixed(4)} / $${item.total_actual_cost.toFixed(4)}`
    })
  ].join('\n')
)

const PLATFORM_LABELS: Record<string, string> = {
  anthropic: 'Claude',
  openai: 'OpenAI',
  gemini: 'Gemini',
  antigravity: 'Antigravity',
  grok: 'Grok',
  kimi: 'Kimi',
  zhipu: 'Zhipu GLM',
  deepseek: 'DeepSeek',
  minimax: 'MiniMax',
}

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform
}
</script>
