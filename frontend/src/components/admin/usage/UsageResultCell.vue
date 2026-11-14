<template>
  <span class="inline-flex items-center gap-1.5 whitespace-nowrap text-sm" data-usage-result>
    <span :class="resultClass(row.result)">{{ resultWord(row.result, t) }}</span>
    <span v-if="row.fallback" class="usage-mark" data-usage-mark="fallback">{{ t('admin.schedQuality.usage.fallback') }}</span>
    <span v-if="row.takeover" class="usage-mark" data-usage-mark="takeover">{{ t('admin.schedQuality.usage.takeover') }}</span>
    <span v-if="row.switches" class="usage-mark" data-usage-mark="switches">{{ t('admin.schedQuality.usage.switches', { n: row.switches }) }}</span>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AdminUsageLog } from '@/types'
import { resultClass, resultWord } from '@/utils/usageResult'

// 结果 of one request (FINAL §6): the word, then 兜底, 接管 and 换号 n 次 when they apply.
defineProps<{ row: Pick<AdminUsageLog, 'result' | 'fallback' | 'takeover' | 'switches'> }>()
const { t } = useI18n()
</script>

<style scoped>
.usage-mark {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  border-radius: 4px;
  border: 1px solid var(--sq-line);
  color: var(--sq-text-2);
  font-size: 11px;
  line-height: 16px;
}
</style>
