<template>
  <div class="recent" :class="{ 'recent--card': card }" data-test="account-recent">
    <template v-for="line in lines" :key="line.dim">
      <span class="recent__k">{{ line.label }}</span>
      <span class="recent__v" :class="line.cls" :data-dim="line.dim" v-tip="line.tip">{{ line.text }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * Recent status (prototype): first token, errors and cache on three lines, from the account's quality summary. A value
 * over the line (the backend's `red` mark: one level only) is amber with a dotted underline, never red; too few samples
 * or no data reads muted. The browser judges nothing against a threshold. In the narrow card (`card`) the three sit
 * side by side, each label over its value; the thresholds are in the quality panel.
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { BackendAccountSummary, BackendMark } from '@/api/admin/schedQualityTypes'
import { vTip } from '@/utils/tip'
import { formatRecentPercent, formatRecentSeconds } from './accountRecent'

const props = defineProps<{ quality: Pick<BackendAccountSummary, 'ttfo' | 'error' | 'cache' | 'stop'> | null; card?: boolean }>()
const { t } = useI18n()

type Dim = 'ttfo' | 'error' | 'cache'
const lines = computed(() => {
  const q = props.quality
  const row = (dim: Dim, mark: BackendMark | undefined, value: number | null | undefined, format: (v: number) => string) => {
    const label = t(`admin.accounts.table.dims.${dim}`)
    if (!mark || value == null) return { dim, label, text: '—', cls: 'recent__v--na', tip: mark ? t('admin.accounts.table.recentNone', { label }) : '' }
    const text = format(value)
    if (mark.short) return { dim, label, text, cls: 'recent__v--na', tip: t('admin.accounts.table.recentShort', { label, n: mark.n }) }
    if (mark.red && !q?.stop) return { dim, label, text, cls: 'recent__v--over', tip: t('admin.accounts.table.recentOver', { label, value: text }) }
    const tip = [t('admin.accounts.table.recentValue', { label, value: text }), q?.stop ? t('admin.accounts.table.recentStopped') : ''].filter(Boolean).join('\n')
    return { dim, label, text, cls: '', tip }
  }
  return [
    row('ttfo', q?.ttfo, q?.ttfo.p50_ms, formatRecentSeconds),
    row('error', q?.error, q?.error.rate, formatRecentPercent),
    row('cache', q?.cache, q?.cache.ratio, (v) => `${Math.round(v * 100)}%`)
  ]
})
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
@media (min-width: 1680px) {
  .recent {
    width: 84px;
  }
}
.recent__k {
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: var(--line-height-body);
}
.recent__v {
  color: var(--color-text-primary);
  font-size: var(--font-size-body);
  line-height: var(--line-height-body);
  text-align: right;
  white-space: nowrap;
}
.recent__v--over {
  color: var(--color-status-back-text);
  text-decoration: underline dotted 1px;
  text-underline-offset: 3px;
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
