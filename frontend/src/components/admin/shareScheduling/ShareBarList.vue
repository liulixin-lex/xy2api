<template>
  <div>
    <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ title }}</h4>
    <ul v-if="rows.length" class="mt-2 space-y-1.5">
      <li v-for="row in rows" :key="row.key" class="text-xs">
        <div class="flex items-center justify-between gap-2 text-gray-700 dark:text-dark-200">
          <span class="min-w-0 truncate" :title="row.key">{{ row.label }}</span>
          <span class="shrink-0 tabular-nums">{{ row.display }} · {{ formatSharePercent(row.share) }}</span>
        </div>
        <div class="mt-0.5 h-1.5 rounded-full bg-gray-100 dark:bg-dark-700">
          <div class="h-1.5 rounded-full" :class="barClass" :style="{ width: Math.max(row.share * 100, 1) + '%' }" />
        </div>
      </li>
    </ul>
    <p v-else class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ empty }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatSharePercent } from '@/utils/shareScheduling'
import { formatNumber } from '@/utils/format'

const props = withDefaults(defineProps<{
  title: string
  values: Record<string, number> | undefined
  label: (key: string) => string
  empty: string
  barClass?: string
}>(), { barClass: 'bg-primary-500' })

const rows = computed(() => {
  const entries = Object.entries(props.values ?? {}).filter(([, value]) => value > 0)
  const total = entries.reduce((sum, [, value]) => sum + value, 0)
  return entries
    .sort((a, b) => b[1] - a[1])
    .map(([key, value]) => ({ key, label: props.label(key), display: formatNumber(value), share: total > 0 ? value / total : 0 }))
})
</script>
