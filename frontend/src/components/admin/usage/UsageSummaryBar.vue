<template>
  <div class="card grid grid-cols-2 divide-gray-100 sm:grid-cols-4 lg:grid-cols-7 lg:divide-x dark:divide-dark-700" data-usage-summary>
    <div v-for="item in items" :key="item.key" class="px-4 py-3" :data-usage-summary-item="item.key">
      <p class="text-xs font-medium text-gray-500 dark:text-dark-400">{{ item.label }}</p>
      <p :class="['mt-1 text-xl font-semibold tabular-nums', item.cls]">{{ item.value }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import { DASH, fmtCacheRatio, fmtSeconds } from '@/utils/schedQualityFormat'

// Summary of the usage page (FINAL §7): requests, 成功 / 已救回 / 失败, user-side first output P50, cache, cost; all
// under the same filter as the list.
const props = defineProps<{ stats: AdminUsageStatsResponse | null }>()
const { t } = useI18n()

const count = (n: number | undefined) => (n == null ? DASH : n.toLocaleString())
const items = computed(() => {
  const s = props.stats
  return [
    { key: 'requests', value: count(s?.total_requests), cls: '' },
    { key: 'success', value: count(s?.success), cls: '' },
    { key: 'rescued', value: count(s?.rescued), cls: '' },
    { key: 'failed', value: count(s?.failed), cls: s?.failed ? 'sq-cool' : '' },
    { key: 'ttfo', value: fmtSeconds(s?.ttfo_p50_ms), cls: '' },
    { key: 'cache', value: fmtCacheRatio(s?.cache_ratio), cls: '' },
    { key: 'cost', value: s ? `$${s.total_actual_cost.toFixed(4)}` : DASH, cls: '' }
  ].map((item) => ({ ...item, label: t(`admin.schedQuality.usage.summary.${item.key}`) }))
})
</script>
