<template>
  <component
    :is="link ? RouterLink : 'div'"
    v-bind="link ? { to: { path: '/admin/usage', query: { account_id: String(accountId) } } } : {}"
    v-tip.focus="link ? focusTip : ''"
    :class="['acc-recent', link ? 'acc-recent--link sq-focus' : '']"
    :aria-label="ariaLabel"
    data-test="recent-stats"
  >
    <template v-if="loading && !stats">
      <span v-for="dim in DIMS" :key="dim" class="skeleton acc-recent__sk" data-test="recent-skeleton" />
    </template>
    <span v-else-if="!lines.some((line) => line.value !== null)" class="acc-recent__empty" data-test="recent-empty">{{ DASH }}</span>
    <template v-else>
      <template v-for="line in lines" :key="line.dim">
        <span class="acc-recent__l">{{ line.label }}</span>
        <span
          v-tip="line.n === null ? '' : t('admin.schedQuality.recent.samples', { n: line.n })"
          :class="['acc-recent__v', line.value === null ? 'acc-recent__v--none' : line.tier ? `acc-recent__v--${line.tier}` : '']"
          :data-test="`recent-${line.dim}`"
        >{{ line.text }}</span>
      </template>
    </template>
  </component>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import type { AccountRecentStats, RecentStatsTier } from '@/types/schedQuality'
import { vTip } from '@/utils/tip'

// 近期状况 (contract delta C, ui-proposal section 2): first token, errors and cache in three fixed lines. The
// colour comes only from the backend's tier. The cell keeps one height for values, gaps, nothing and loading, so
// the stats arriving after the list never move the rows.
const props = defineProps<{
  accountId: number
  stats?: AccountRecentStats
  loading?: boolean
  /** The viewer may open usage records: the whole cell links to this account's records. */
  link?: boolean
}>()
const { t } = useI18n()

const DASH = '—'
const DIMS = ['ttft', 'error', 'cache'] as const
type Dim = (typeof DIMS)[number]
const LABEL_KEY: Record<Dim, string> = {
  ttft: 'admin.schedQuality.dim.ttfo',
  error: 'admin.schedQuality.dim.error',
  cache: 'admin.schedQuality.dim.cache'
}

const fmtSeconds = (ms: number) => {
  const s = ms / 1000
  return s < 100 ? `${s.toFixed(1)}s` : `${Math.round(s)}s`
}
const fmtErrorPct = (rate: number) => {
  const pct = rate * 100
  if (pct === 0) return '0%'
  return pct < 10 ? `${pct.toFixed(1)}%` : `${Math.round(pct)}%`
}
const fmtPct = (ratio: number) => `${Math.round(ratio * 100)}%`

interface Line {
  dim: Dim
  label: string
  value: number | null
  text: string
  tier: RecentStatsTier
  /** Sample count; null when the metric is off (errors while ops monitoring is off). */
  n: number | null
}

const lines = computed<Line[]>(() => {
  const s = props.stats
  const line = (dim: Dim, value: number | null | undefined, tier: RecentStatsTier | undefined, n: number | null, fmt: (v: number) => string): Line => ({
    dim,
    label: t(LABEL_KEY[dim]),
    value: value ?? null,
    text: value == null ? DASH : fmt(value),
    tier: tier ?? null,
    n
  })
  return [
    line('ttft', s?.ttft.p50_ms, s?.ttft.tier, s ? s.ttft.n : null, fmtSeconds),
    line('error', s?.error?.rate, s?.error?.tier, s?.error ? s.error.n : null, fmtErrorPct),
    line('cache', s?.cache.ratio, s?.cache.tier, s ? s.cache.n : null, fmtPct)
  ]
})

// Keyboard focus on the link shows the three sample counts; the tip is also its aria description while shown.
const focusTip = computed(() =>
  lines.value
    .filter((line) => line.n !== null)
    .map((line) => `${line.label} ${t('admin.schedQuality.recent.samples', { n: line.n })}`)
    .join('\n')
)
const ariaLabel = computed(() =>
  lines.value.some((line) => line.value !== null)
    ? lines.value.map((line) => `${line.label} ${line.text}`).join(t('admin.schedQuality.recent.sep'))
    : `${t('admin.schedQuality.list.recent')} ${DASH}`
)
</script>

<style scoped>
.acc-recent {
  display: grid;
  grid-template-columns: max-content minmax(44px, 1fr);
  column-gap: 12px;
  align-content: center;
  min-width: 112px;
  min-height: 54px;
  margin: 0 -4px;
  padding: 0 4px;
  border-radius: 6px;
  color: inherit;
  text-decoration: none;
}
.acc-recent--link:hover { background: rgba(127, 127, 127, 0.08); }
.acc-recent--link:focus-visible { outline-offset: 0; }
.acc-recent__l { font-size: 12px; line-height: 18px; color: var(--sq-text-3); }
.acc-recent__v {
  font-size: 13px;
  line-height: 18px;
  font-weight: 500;
  text-align: right;
  font-variant-numeric: tabular-nums;
  color: var(--sq-text);
}
.acc-recent__v--good { color: var(--sq-tier-good); }
.acc-recent__v--mid { color: var(--sq-tier-mid); }
.acc-recent__v--bad { color: var(--sq-tier-bad); font-weight: 600; }
.acc-recent__v--none { color: var(--sq-text-3); font-weight: 400; }
.acc-recent__empty { grid-column: 1 / -1; font-size: 13px; line-height: 18px; text-align: right; color: var(--sq-text-3); }
.acc-recent__sk { grid-column: 1 / -1; justify-self: end; width: 32px; height: 10px; margin: 4px 0; }
</style>
