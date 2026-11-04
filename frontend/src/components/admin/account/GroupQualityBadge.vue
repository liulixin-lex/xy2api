<template>
  <span
    data-testid="group-quality-badge"
    :data-state="quality.kind"
    :class="[
      'inline-flex min-w-0 max-w-48 items-center gap-1 rounded-md px-2 py-0.5 text-xs font-medium',
      GROUP_QUALITY_BADGE_CLASSES[quality.kind]
    ]"
  >
    <template v-if="pinned">
      <Icon name="lock" size="xs" class="shrink-0" aria-hidden="true" />
      <span class="sr-only">{{ t('admin.groupQualityGate.pinnedMark') }}</span>
    </template>
    <span class="min-w-0 truncate">{{ name }}</span>
    <span class="shrink-0 whitespace-nowrap rounded bg-white/70 px-1.5 text-[11px] font-semibold leading-4 dark:bg-white/10">{{ stateLabel }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import {
  GROUP_QUALITY_BADGE_CLASSES,
  groupQualityStateLabel,
  isGroupQualityNotice,
  type GroupQualityCell
} from './groupQualityPresentation'

// 开启质量门槛的分组标签：颜色表示该账号在这个组里的状态；说明由所在单元格的提示统一给出。
// 状态小签用浅色底（白 70%）加 11px 粗体，浅色模式下对比度达到 WCAG AA。
const props = defineProps<{
  name: string
  quality: GroupQualityCell
}>()

const { t } = useI18n()

const pinned = computed(() => !isGroupQualityNotice(props.quality) && props.quality.account.pinned)
const stateLabel = computed(() => groupQualityStateLabel(props.quality, t))
</script>
