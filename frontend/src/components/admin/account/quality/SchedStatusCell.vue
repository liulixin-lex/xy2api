<template>
  <span v-if="loading && !summary" class="skeleton inline-block h-3.5 w-12 align-middle" />
  <span v-else-if="!summary" class="sq-text-3">—</span>
  <span v-else :class="['whitespace-nowrap text-sm', cls]" data-sq-status>{{ text }}</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedAccountSummary } from '@/types/schedQuality'
import { statusText } from '@/utils/schedQualityFormat'

// Status column (FINAL §2, §7): 正常 / 观察中 / 停用·reason·until. Automatic stops are red, manual ones grey,
// 观察中 grey (its sample is short).
const props = defineProps<{ summary?: SchedAccountSummary; loading?: boolean }>()
const { t, te } = useI18n()

const text = computed(() => (props.summary ? statusText(props.summary, Date.now(), t, te) : ''))
const cls = computed(() => {
  const s = props.summary
  if (s?.status === 'disabled') return s.stop?.source === 'manual' ? 'sq-text-3' : 'sq-cool'
  return s?.status === 'observing' ? 'sq-text-3' : 'sq-text'
})
</script>
