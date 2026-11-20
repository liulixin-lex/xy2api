<template>
  <div ref="rootRef" class="today" data-test="account-today">
    <template v-for="line in lines" :key="line.key">
      <span class="today__k" :data-label="line.key" v-tip="() => labelTip(line.key, line.label)">{{ line.label }}</span>
      <span class="today__v" :class="{ 'today__v--na': line.text === NO_VALUE }" :data-key="line.key" v-tip="line.text !== line.exact ? line.exact : ''" @click="line.text !== line.exact && showTipNow($event.currentTarget)">{{ line.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Today's stats (spec 7a, 7b): four lines (requests, tokens, account cost, user charge), labels left and values right.
 * Counts are compact (three significant digits, the full value in the tip; a tap shows it at once, touch has no
 * hover); amounts are exact, two decimals with thousands separators (mobile list plan: "$1,234.56", not "$1.23K").
 * A value never gives way: a label that does not fit ends in "…" (English), its tip has it whole. Before the stats are
 * in, every line reads a light "—", not 0 (7d-6).
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import {
  formatCompactRequests,
  formatCompactTokens,
  formatExactCount,
  formatExactMoney
} from '@/utils/compactNumber'
import { showTipNow, vTip } from '@/utils/tip'

const props = defineProps<{ stats: WindowStats | null | undefined }>()
const NO_VALUE = formatExactCount(null)
const rootRef = ref<HTMLElement | null>(null)
/** A label's tip only while it is cut. */
const labelTip = (key: string, label: string) => {
  const el = rootRef.value?.querySelector<HTMLElement>(`[data-label="${key}"]`)
  return el && el.scrollWidth > el.clientWidth + 1 ? label : ''
}
const { t } = useI18n()

const lines = computed(() => {
  const s = props.stats
  return [
    { key: 'requests', label: t('admin.accounts.table.todayRequests'), text: formatCompactRequests(s?.requests), exact: formatExactCount(s?.requests) },
    { key: 'tokens', label: t('admin.accounts.table.todayTokens'), text: formatCompactTokens(s?.tokens), exact: formatExactCount(s?.tokens) },
    { key: 'cost', label: t('admin.accounts.table.todayCost'), text: formatExactMoney(s?.cost), exact: formatExactMoney(s?.cost) },
    { key: 'user_cost', label: t('admin.accounts.table.todayUserCost'), text: formatExactMoney(s?.user_cost), exact: formatExactMoney(s?.user_cost) }
  ]
})
</script>

<style scoped>
.today {
  display: grid;
  grid-template-columns: minmax(0, max-content) minmax(max-content, 1fr);
  column-gap: 6px;
  row-gap: 1px;
  font-size: var(--font-size-small);
  line-height: 15px;
}
.today__k {
  min-width: 0;
  overflow: hidden;
  color: var(--color-text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
}
.today__v {
  color: var(--color-text-primary);
  font-variant-numeric: tabular-nums;
  text-align: right;
  white-space: nowrap;
}
.today__v--na {
  color: var(--color-text-muted);
}
</style>
