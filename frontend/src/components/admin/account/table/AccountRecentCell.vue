<template>
  <div class="recent sq-focus" :class="{ 'recent--card': card }" :tabindex="quality ? 0 : undefined" data-test="account-recent" v-tip="tipCard">
    <template v-for="line in lines" :key="line.dim">
      <span class="recent__k">{{ line.label }}</span>
      <span class="recent__v" :class="{ 'recent__v--na': line.muted }" :data-dim="line.dim">
        <template v-if="line.spoken"><span aria-hidden="true">{{ line.text }}</span><span class="sr-only">{{ line.spoken }}</span></template>
        <template v-else>{{ line.text }}</template>
      </span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Recent status: first token, errors and cache on three lines, from the account's quality summary. These are the
 * whole account's values over the last hour, so they are never coloured by a mark that judges one group and model
 * (7k-2): which group, model and line fall short is the group dots' and their tips' to tell. First token is the
 * median first output with timeouts counted last ("超时" when more than half timed out), cache the continuation
 * turns. Too few samples or no data reads muted. In the narrow card (`card`) the three sit side by side, each label
 * over its value. The whole cell has one tip card (12.7): the window, then four rows with their samples, the same as
 * the quality panel's recent section. With a tip the cell takes keyboard focus, which shows the card and points
 * aria-describedby at it (v-tip); without quality data there is nothing to focus. The shared ring sits outside the
 * content; in the table, whose cell clips outside 72px, AccountsView draws it on the column's cell instead.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { vTip, type TipCard } from '@/utils/tip'
import { RECENT_DIMS, type RecentQuality, firstOutputMedian, recentRows, recentTitle } from './accountRecent'

const props = defineProps<{ quality: RecentQuality | null; card?: boolean }>()
const { t } = useI18n()

const lines = computed(() => {
  const q = props.quality
  const rows = recentRows(q, t)
  return RECENT_DIMS.map((dim) => {
    // The 72px table cell takes the short labels (English "Errors"/"Cache" do not fit); the card has room for the full ones.
    const label = t(`admin.accounts.table.${props.card ? 'dims' : 'dimsShort'}.${dim}`)
    const row = rows.find((item) => item.key === dim)!
    // English "Timeout" does not fit the 72px table cell: it shows "T/O", and a screen reader reads the word (Chinese
    // "超时" is the same either way, nothing to add).
    if (!props.card && q && dim === 'ttfo' && firstOutputMedian(q) === 'timeout') {
      const short = t('admin.accounts.table.recent.timeoutShort')
      return { dim, label, text: short, spoken: short === row.text ? '' : row.text, muted: row.muted }
    }
    return { dim, label, text: row.text, spoken: '', muted: row.muted }
  })
})

/** The window and the four rows; no tip without quality data. */
function tipCard(): TipCard | '' {
  const quality = props.quality
  if (!quality) return ''
  return {
    title: recentTitle(quality, t),
    // A narrow screen wraps the samples rather than cut a label or a value.
    wrapEnd: true,
    rows: recentRows(quality, t).map((row) => [row.label, row.muted ? { text: row.text, tone: 'muted' as const } : row.text, row.note])
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
