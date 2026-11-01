<template>
  <div
    v-if="items.length"
    data-testid="account-quality-metrics"
    class="mt-1 flex max-w-56 items-baseline overflow-hidden whitespace-nowrap text-[11px] leading-4 tabular-nums text-gray-500 dark:text-gray-400"
  >
    <template v-for="(item, index) in items" :key="item.key">
      <span v-if="index" class="mx-1 text-gray-300 dark:text-dark-500" aria-hidden="true">·</span>
      <span
        :data-metric="item.key"
        data-alert="true"
        class="font-semibold text-red-600 dark:text-red-400"
        :title="item.title"
      >{{ item.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import { accountQualityMetricItems, type QualityLimits } from './groupQualityPresentation'

// 分组单元格下方的指标行只在异常时出现：只列超过所在分组最严上限（零缓存为 30%）的项。
// 三项指标的完整一行在分组门槛面板底部。
const props = defineProps<{ metrics: GroupQualityAccountMetrics; limits?: QualityLimits }>()

const { t } = useI18n()
const items = computed(() => accountQualityMetricItems(props.metrics, t, props.limits).filter(item => item.alert))
</script>
