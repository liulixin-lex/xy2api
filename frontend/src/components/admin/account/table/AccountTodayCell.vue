<template>
  <div class="today" :class="{ 'today--full': full }" data-test="account-today">
    <template v-for="line in lines" :key="line.key">
      <span class="today__k">{{ line.label }}</span>
      <span v-if="full" class="today__v" :data-key="line.key">{{ line.exact }}</span>
      <span v-else class="today__v" :data-key="line.key" v-tip="line.text !== line.exact ? line.exact : ''">{{ line.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Today's stats (spec 7a, 7b): four compact lines (requests, tokens, account cost, user charge), labels left and values
 * right, every number on the same three-significant-digit rule; the full value shows on hover. Before the stats are in,
 * every line reads "—", not 0 (7d-6). The narrow card's details (`full`) show the full values instead: touch screens
 * have no hover.
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

const props = defineProps<{ stats: WindowStats | null | undefined; full?: boolean }>()
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
/* Full values can be long ("$1,234,567.89"): one pair to a line, the value left after its label. */
.today--full {
  column-gap: 12px;
  row-gap: 4px;
  font-size: var(--font-size-body);
  line-height: 18px;
}
.today--full .today__k {
  font-size: var(--font-size-small);
}
.today--full .today__v {
  text-align: left;
}
</style>
