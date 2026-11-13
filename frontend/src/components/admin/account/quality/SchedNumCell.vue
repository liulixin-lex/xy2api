<template>
  <span v-if="loading && !summary" class="skeleton ml-auto block h-3.5 w-10" />
  <span v-else-if="!summary || !mark || mark.n === 0" class="sq-text-3">—</span>
  <span v-else class="sq-num inline-flex flex-col items-end leading-tight" :data-sq-num="dim">
    <span :class="markClass(mark)">{{ markText(summary, dim) }}</span>
    <span class="text-[11px] sq-text-3">{{ t('admin.schedQuality.samples', { n: mark.n }) }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedAccountSummary, SchedDim } from '@/types/schedQuality'
import { markClass, markText } from '@/utils/schedQualityFormat'

// One account-level number of the list (FINAL §6): the value over its sample count, coloured by markClass.
const props = defineProps<{ summary?: SchedAccountSummary; dim: SchedDim; loading?: boolean }>()
const { t } = useI18n()
const mark = computed(() => props.summary?.[props.dim])
</script>
