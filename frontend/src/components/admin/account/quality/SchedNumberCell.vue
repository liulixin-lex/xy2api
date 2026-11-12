<template>
  <span v-if="loading" class="skeleton ml-auto inline-block h-3.5 w-10 align-middle" />
  <button
    v-else
    type="button"
    v-tip="tip"
    :tabindex="focusable ? 0 : -1"
    :class="['sq-plain sq-focus text-sm', text === DASH ? 'sq-text-3' : 'sq-text']"
    :data-sq-trigger="`num:${accountId}:${dim}`"
    @click.stop="emit('open')"
  >{{ text }}</button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedAccountSummary } from '@/types/schedQuality'
import { DASH, fmtCacheRatio, fmtErrorRate, fmtTtfo, type SchedDim } from '@/utils/schedQualityFormat'
import { vTip } from '@/utils/tip'

// One of the three neutral numbers of the account list (design spec A.1, A.4): never coloured; the tip names the
// model and sample count behind it. Only the first-output number is in the Tab order (A.8).
const props = defineProps<{
  accountId: number
  dim: SchedDim
  summary?: SchedAccountSummary
  loading?: boolean
  focusable?: boolean
}>()
const emit = defineEmits<{ open: [] }>()
const { t } = useI18n()

const text = computed(() => {
  const s = props.summary
  if (!s) return DASH
  if (props.dim === 'ttfo') return fmtTtfo(s.ttfo.p90_ms, s.ttfo.timeout, t)
  return props.dim === 'error' ? fmtErrorRate(s.error.rate) : fmtCacheRatio(s.cache.ratio)
})

const tip = computed(() => {
  const s = props.summary
  if (!s?.model) return ''
  return t('admin.schedQuality.tip.source', { model: s.model, n: s[props.dim].n })
})
</script>
