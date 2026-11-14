<template>
  <div class="today" data-test="account-today">
    <template v-for="line in lines" :key="line.key">
      <span class="today__k">{{ line.label }}</span>
      <span class="today__v" :data-key="line.key" v-tip="line.text !== line.exact ? line.exact : ''">{{ line.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Today's stats (spec 7a, 7b): four compact lines (requests, tokens, account cost, user charge), labels left and values
 * right, every number on the same three-significant-digit rule; the full value shows on hover. Before the stats are in,
 * every line reads "—", not 0 (7d-6).
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import {
  formatCompactMoney,
  formatCompactRequests,
  formatCompactTokens,
  formatExactCount,
  formatExactMoney
} from '@/utils/compactNumber'
import { vTip } from '@/utils/tip'

const props = defineProps<{ stats: WindowStats | null | undefined }>()
const { t } = useI18n()

const lines = computed(() => {
  const s = props.stats
  return [
    { key: 'requests', label: t('admin.accounts.table.todayRequests'), text: formatCompactRequests(s?.requests), exact: formatExactCount(s?.requests) },
    { key: 'tokens', label: t('admin.accounts.table.todayTokens'), text: formatCompactTokens(s?.tokens), exact: formatExactCount(s?.tokens) },
    { key: 'cost', label: t('admin.accounts.table.todayCost'), text: formatCompactMoney(s?.cost), exact: formatExactMoney(s?.cost) },
    { key: 'user_cost', label: t('admin.accounts.table.todayUserCost'), text: formatCompactMoney(s?.user_cost), exact: formatExactMoney(s?.user_cost) }
  ]
})
</script>

<style scoped>
.today {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  column-gap: 8px;
  row-gap: 1px;
  font-size: var(--font-size-small);
  line-height: 15px;
}
.today__k {
  color: var(--color-text-secondary);
  white-space: nowrap;
}
.today__v {
  color: var(--color-text-primary);
  text-align: right;
  white-space: nowrap;
}
</style>
