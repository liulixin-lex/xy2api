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
 * are the whole account's values over the last hour, so they are never coloured by a mark that judges one group and
 * model (7k-2): which group, model and line fall short is the group dots' and their tips' to tell. First token is
 * the first useful output and cache the continuation turns, as the quality lines judge them. Too few samples or no
 * data reads muted. In the narrow card (`card`) the three sit side by side, each label over its value; the thresholds
 * are in the quality panel. The whole cell has one tip card (12.7): the window, then each value with its samples,
 * the first event and its gap to the first useful output, and the cache ratio of every successful request.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendAccountSummary, BackendDim } from '@/api/admin/schedQualityTypes'
import { vTip, type TipCard } from '@/utils/tip'
import { RECENT_DIMS, formatRecentSecondsCompact, recentExtras, recentValue } from './accountRecent'
import { formatShortTime } from './accountTime'

const props = defineProps<{ quality: Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache' | 'reset_at'> | null; card?: boolean }>()
const { t } = useI18n()

const HOUR_MS = 3_600_000

const lines = computed(() => {
  const q = props.quality
  return RECENT_DIMS.map((dim: BackendDim) => {
    // The 72px table cell takes the short labels (English "Errors"/"Cache" do not fit); the card has room for the full ones.
    const label = t(`admin.accounts.table.${props.card ? 'dims' : 'dimsShort'}.${dim}`)
    const value = q ? recentValue(q, dim) : null
    if (!value || value.text === null) return { dim, label, value, text: '—', full: '—', muted: true }
    // The cell is 72px: first token is written compactly there (0.10s), the tip card keeps the full text (100ms).
    const ms = dim === 'ttfo' ? q?.ttfo.first_output_p50_ms : null
    const text = ms == null ? value.text : formatRecentSecondsCompact(ms)
    return { dim, label, value, text, full: value.text, muted: value.short }
  })
})

const COUNT_KEYS: Record<BackendDim, string> = { ttfo: 'count', error: 'count', cache: 'turns' }

/** "近 1 小时", or "HH:MM 起" when the account's stats were reset within the hour; no tip without quality data. */
function tipCard(): TipCard | '' {
  const quality = props.quality
  if (!quality) return ''
  const reset = quality.reset_at ? Date.parse(quality.reset_at) : Number.NaN
  const title = reset > Date.now() - HOUR_MS
    ? t('admin.accounts.table.tip.recentSince', { time: formatShortTime(quality.reset_at) })
    : t('admin.accounts.table.tip.recentTitle')
  const extras = recentExtras(quality)
  const plain = (text: string | null) => (text === null ? { text: '—', tone: 'muted' as const } : text)
  const valueRow = (line: (typeof lines.value)[number], label: string) => {
    const n = line.value?.n ?? 0
    const key = `admin.accounts.table.tip.${COUNT_KEYS[line.dim]}${line.muted ? 'Short' : ''}`
    return [label, line.muted ? { text: line.full, tone: 'muted' as const } : line.full, t(key, { n }, n)]
  }
  const [ttfo, error, cache] = lines.value
  return {
    title,
    rows: [
      valueRow(ttfo, t('admin.accounts.table.recent.firstOutput')),
      [t('admin.accounts.table.recent.firstEvent'), plain(extras.firstEvent), ''],
      [t('admin.accounts.table.recent.gap'), plain(extras.gap), ''],
      valueRow(error, t('admin.accounts.table.dims.error')),
      valueRow(cache, t('admin.accounts.table.recent.cacheTurns')),
      [
        t('admin.accounts.table.recent.cacheAll'),
        plain(extras.cacheAll),
        t('admin.accounts.table.tip.count', { n: extras.cacheAllRequests }, extras.cacheAllRequests)
      ]
    ]
  }
}
</script>

<style scoped>
.recent {
  display: grid;
  /* 72px = label ≤ 25 (two 12px characters, or "First") + gap 6 + value ≤ 36.3 ("100%"), measured with the system
     fonts; were a label still wider, it gives way (ellipsis), never the value. */
  grid-template-columns: minmax(0, auto) auto;
  justify-content: space-between;
  column-gap: 6px;
  row-gap: 2px;
  width: 72px;
}
.recent__k {
  overflow: hidden;
  color: var(--color-text-muted);
  text-overflow: ellipsis;
  white-space: nowrap;
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
