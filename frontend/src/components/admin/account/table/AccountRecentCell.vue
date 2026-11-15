<template>
  <div class="recent" :class="{ 'recent--card': card }" data-test="account-recent" v-tip="tipCard">
    <template v-for="line in lines" :key="line.dim">
      <span class="recent__k">{{ line.label }}</span>
      <span class="recent__v" :class="{ 'recent__v--na': line.muted }" :data-dim="line.dim">{{ line.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Recent status (prototype): first token, errors and cache on three lines, from the account's quality summary. These
 * are the whole account's values over the last hour, so they are never coloured by the summary's `red` mark, which
 * judges one group and model (7k-2): which group, model and dimension fall short is the group dots' and their tips'
 * to tell. Too few samples or no data reads muted. In the narrow card (`card`) the three sit side by side, each label
 * over its value; the thresholds are in the quality panel. The whole cell has one tip card (12.7): the window, then
 * each dimension's value and sample count.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendAccountSummary, BackendMark } from '@/api/admin/schedQualityTypes'
import { vTip, type TipCard } from '@/utils/tip'
import { formatRecentPercent, formatRecentSeconds } from './accountRecent'
import { formatShortTime } from './accountTime'

const props = defineProps<{ quality: Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache' | 'reset_at'> | null; card?: boolean }>()
const { t } = useI18n()

const HOUR_MS = 3_600_000

type Dim = 'ttfo' | 'error' | 'cache'
const lines = computed(() => {
  const q = props.quality
  const row = (dim: Dim, mark: BackendMark | undefined, value: number | null | undefined, format: (v: number) => string) => {
    const label = t(`admin.accounts.table.dims.${dim}`)
    if (!mark || value == null) return { dim, label, mark, text: '—', muted: true }
    return { dim, label, mark, text: format(value), muted: mark.short }
  }
  return [
    row('ttfo', q?.ttfo, q?.ttfo.p50_ms, formatRecentSeconds),
    row('error', q?.error, q?.error.rate, formatRecentPercent),
    row('cache', q?.cache, q?.cache.ratio, (v) => `${Math.round(v * 100)}%`)
  ]
})

/** "近 1 小时", or "HH:MM 起" when the account's stats were reset within the hour; no tip without quality data. */
function tipCard(): TipCard | '' {
  const quality = props.quality
  if (!quality) return ''
  const reset = quality.reset_at ? Date.parse(quality.reset_at) : Number.NaN
  const title = reset > Date.now() - HOUR_MS
    ? t('admin.accounts.table.tip.recentSince', { time: formatShortTime(quality.reset_at) })
    : t('admin.accounts.table.tip.recentTitle')
  return {
    title,
    rows: lines.value.map((line) => {
      const n = line.mark?.n ?? 0
      return [
        line.label,
        line.muted ? { text: line.text, tone: 'muted' } : line.text,
        t(line.mark?.short ? 'admin.accounts.table.tip.countShort' : 'admin.accounts.table.tip.count', { n }, n)
      ]
    })
  }
}
</script>

<style scoped>
.recent {
  display: grid;
  grid-template-columns: auto auto;
  justify-content: space-between;
  column-gap: 12px;
  row-gap: 2px;
  width: 72px;
}
.recent__k {
  color: var(--color-text-muted);
  font-size: var(--font-size-small);
  line-height: var(--line-height-body);
}
.recent__v {
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  font-variant-numeric: tabular-nums;
  line-height: var(--line-height-body);
  text-align: right;
  white-space: nowrap;
}
.recent__v--na {
  color: var(--color-text-muted);
}
.recent--card {
  grid-template-rows: auto auto;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  grid-auto-flow: column;
  justify-content: stretch;
  width: auto;
}
.recent--card .recent__v {
  text-align: left;
}
</style>
