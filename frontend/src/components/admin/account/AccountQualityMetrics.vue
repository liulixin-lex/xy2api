<template>
  <div
    v-if="items.length"
    data-testid="account-quality-metrics"
    class="mt-1 flex max-w-64 flex-wrap items-baseline text-[11px] leading-4 tabular-nums text-gray-500 dark:text-gray-400"
  >
    <template v-for="(item, index) in items" :key="item.key">
      <span v-if="index" class="mx-1 text-gray-300 dark:text-dark-500" aria-hidden="true">·</span>
      <span
        :data-metric="item.key"
        :data-tone="item.tone"
        class="whitespace-nowrap font-semibold"
        :class="item.tone === 'danger' ? 'text-red-600 dark:text-red-400' : 'text-amber-700 dark:text-amber-400'"
        :title="item.title"
      >{{ item.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import { QUALITY_CLOCK, accountQualityAlertItems, type GroupQualityCell } from './groupQualityPresentation'

// 分组单元格下方的提示行只在异常时出现：某维在任一门槛分组里不达标时列出该维数值，判慢让位时列出到期时刻。
// 各组三维的完整状态在分组门槛面板里。
const props = defineProps<{ quality?: Map<number, GroupQualityCell> }>()

const { t } = useI18n()
const clock = (iso: string) => formatDateTime(iso, QUALITY_CLOCK)
const items = computed(() => accountQualityAlertItems(props.quality, t, clock))
</script>
