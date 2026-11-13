<template>
  <span v-if="loading && !summary" class="skeleton ml-auto block h-3.5 w-8" />
  <span v-else-if="!summary" class="sq-text-3">—</span>
  <span v-else :class="['sq-num whitespace-nowrap text-sm', cell.cls]" :data-sq-group-cell="col">{{ cell.text }}</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SchedGroupSummary } from '@/types/schedQuality'
import { groupCell, type GroupSchedColumn } from '@/utils/schedQualityFormat'

// One scheduling cell of the group list (FINAL §7): a line, 接管, an account count or a last-60-minutes number.
const props = defineProps<{ col: GroupSchedColumn; summary?: SchedGroupSummary; loading?: boolean }>()
const { t } = useI18n()
const cell = computed(() => groupCell(props.summary!, props.col, t))
</script>
