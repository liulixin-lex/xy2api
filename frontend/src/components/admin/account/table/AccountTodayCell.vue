<template>
  <div
    class="today sq-focus"
    :tabindex="known ? 0 : undefined"
    v-tip="tipCard"
    data-test="account-today"
    @click="known && showTipNow($event.currentTarget)"
  >
    <template v-for="line in lines" :key="line.key">
      <span class="today__k" :data-label="line.key">{{ line.label }}</span>
      <span class="today__v" :class="{ 'today__v--na': line.text === NO_VALUE }" :data-key="line.key">{{ line.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Today's stats (spec 7a, 7b): four lines (requests, tokens, account cost, user charge), labels left and values right.
 * Counts are compact (three significant digits); amounts are exact, two decimals with thousands separators (mobile
 * list plan: "$1,234.56", not "$1.23K"). Labels are short enough to show whole beside "$99,999.99" in the column's
 * 128px (English "Acct cost", "Charged"); a value never gives way, so past that a label ends in "…".
 *
 * The whole cell has one tip card (12.7), on hover, on a tap or click (touch has no hover) and on keyboard focus: every
 * label whole and every value exact. It never depends on measuring what is cut (Astra second re-review #3). Before the
 * stats are in, every line reads a light "—", not 0 (7d-6), and there is nothing to show.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/types'
import {
  formatCompactRequests,
  formatCompactTokens,
  formatExactCount,
  formatExactMoney
} from '@/utils/compactNumber'
import { showTipNow, vTip, type TipCard } from '@/utils/tip'

const props = defineProps<{ stats: WindowStats | null | undefined }>()
const NO_VALUE = formatExactCount(null)
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
/** Stats are in: there is something to show whole. */
const known = computed(() => lines.value.some((line) => line.exact !== NO_VALUE))
/** Every label whole, every value exact; nothing before the stats are in. */
function tipCard(): TipCard | '' {
  if (!known.value) return ''
  return { title: t('admin.accounts.columns.todayStats'), rows: lines.value.map((line) => [line.label, line.exact]) }
}
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
.today[tabindex] {
  cursor: pointer;
}
/* The table cell clips what lies outside it: the shared ring is drawn inside. */
.today.sq-focus:focus-visible {
  outline-offset: -2px;
}
</style>
