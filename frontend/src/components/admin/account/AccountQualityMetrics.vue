<template>
  <div
    data-testid="account-quality-metrics"
    class="mt-1 flex max-w-56 flex-wrap gap-x-2 gap-y-0.5 text-[11px] leading-4 tabular-nums text-gray-500 dark:text-gray-400"
  >
    <span
      v-for="item in items"
      :key="item.key"
      :data-metric="item.key"
      :data-alert="item.alert || undefined"
      :class="item.alert ? 'font-semibold text-red-600 dark:text-red-400' : undefined"
      :title="item.title"
    >{{ item.text }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import { accountQualityMetricItems } from './groupQualityPresentation'

// 账号完整窗口的门槛指标：错误率、续轮首字 p90、续轮零缓存率。
const props = defineProps<{ metrics: GroupQualityAccountMetrics }>()

const { t } = useI18n()
const items = computed(() => accountQualityMetricItems(props.metrics, t))
</script>
