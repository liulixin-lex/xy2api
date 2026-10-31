<template>
  <span
    data-testid="group-quality-badge"
    :data-state="quality.kind"
    :class="[
      'inline-flex min-w-0 max-w-full items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium',
      GROUP_QUALITY_BADGE_CLASSES[quality.kind]
    ]"
    :title="tooltip"
  >
    <template v-if="quality.status.pinned">
      <span aria-hidden="true">📌</span>
      <span class="sr-only">{{ t('admin.groupQualityGate.pinnedMark') }}</span>
    </template>
    <span class="min-w-0 truncate">{{ name }}</span>
    <span class="shrink-0 whitespace-nowrap rounded bg-black/10 px-1.5 py-0.5 text-[10px] font-semibold dark:bg-white/10">{{ stateLabel }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import {
  GROUP_QUALITY_BADGE_CLASSES,
  QUALITY_CLOCK,
  groupQualityStateLabel,
  groupQualityTooltip,
  type AccountGroupQuality
} from './groupQualityPresentation'

// 开启质量门槛的分组标签：颜色表示该账号在这个组里的状态，悬停看原因、实测值、上限与余量。
const props = defineProps<{
  name: string
  quality: AccountGroupQuality
  metrics?: GroupQualityAccountMetrics
}>()

const { t } = useI18n()
const clock = (iso: string) => formatDateTime(iso, QUALITY_CLOCK)

const stateLabel = computed(() => groupQualityStateLabel(props.quality, t, clock))
const tooltip = computed(() => groupQualityTooltip(props.name, props.quality, props.metrics, t, clock).join('\n'))
</script>
