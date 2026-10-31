<template>
  <button
    ref="trigger"
    type="button"
    data-testid="account-groups-cell"
    class="-mx-1 flex max-w-56 flex-wrap items-center gap-1 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
    :aria-label="t('admin.groupCategories.editAccountGroupsAria', { name: accountName })"
    :aria-describedby="details.length ? tooltipId : undefined"
    :title="!details.length && groups.length ? groups.map((group) => group.name).join('、') : undefined"
    @click="emit('open')"
    @mouseenter="openTip"
    @mouseleave="closeTip"
    @focus="openTip"
    @blur="closeTip"
    @keydown.esc="closeTip"
  >
    <template v-if="groups.length">
      <template v-for="group in visibleGroups" :key="group.id">
        <GroupQualityBadge v-if="quality?.get(group.id)" :name="group.name" :quality="quality.get(group.id)!" />
        <GroupBadge
          v-else
          :name="group.name"
          :platform="group.platform"
          :subscription-type="group.subscription_type"
          :rate-multiplier="group.rate_multiplier"
          :show-rate="false"
          class="max-w-24"
        />
      </template>
      <span v-if="hiddenCount > 0" class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400">+{{ hiddenCount }}</span>
    </template>
    <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
  </button>
  <Teleport to="body">
    <div
      v-if="details.length"
      v-show="tipOpen"
      :id="tooltipId"
      role="tooltip"
      data-testid="account-groups-quality-tooltip"
      class="pointer-events-none fixed z-50 w-72 rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-gray-100 shadow-lg dark:bg-dark-800"
      :style="tipStyle"
    >
      <div v-for="(block, index) in details" :key="index" :class="index ? 'mt-2 border-t border-white/10 pt-2' : undefined">
        <p v-for="(line, lineIndex) in block" :key="lineIndex" :class="lineIndex ? 'text-gray-300' : 'font-medium'">{{ line }}</p>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupQualityBadge from './GroupQualityBadge.vue'
import { formatDateTime } from '@/utils/format'
import type { Group } from '@/types'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import {
  QUALITY_CLOCK,
  accountQualityMetricItems,
  groupQualityTooltip,
  orderGroupsByQuality,
  type GroupQualityCell
} from './groupQualityPresentation'

// 账号列表的分组单元格：整格是一个按钮，点开“编辑分组”弹窗；没有分组时显示“—”也能点。
// 开启质量门槛的分组按状态着色，有问题的排在前面。门槛说明（原因、实测值与上限、余量）放在
// 悬停和键盘聚焦都会出现的提示里，并作为按钮的补充描述给读屏。
const TIP_WIDTH = 288
const TIP_GAP = 6

const props = defineProps<{
  groups: Group[]
  accountName: string
  /** groupId → 该账号在开启门槛的分组里的状态 */
  quality?: Map<number, GroupQualityCell>
  metrics?: GroupQualityAccountMetrics
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()
const tooltipId = useId()
const clock = (iso: string) => formatDateTime(iso, QUALITY_CLOCK)

// 有门槛标签时最多显示 2 个（门槛标签更宽），加上一行指标，单元格不超过 3 行。
const maxVisible = computed(() => (props.quality?.size ? 2 : 3))
const orderedGroups = computed(() => orderGroupsByQuality(props.groups, props.quality))
const visibleGroups = computed(() => orderedGroups.value.slice(0, maxVisible.value))
const hiddenCount = computed(() => Math.max(0, props.groups.length - maxVisible.value))

// 每个门槛分组一段（含收进 +N 的），最后一段是账号三项指标的完整名称、时间窗与样本数。
const details = computed(() => {
  const blocks = orderedGroups.value
    .filter((group) => props.quality?.has(group.id))
    .map((group) => groupQualityTooltip(group.name, props.quality!.get(group.id)!, props.metrics, t, clock))
  if (blocks.length && props.metrics) blocks.push(accountQualityMetricItems(props.metrics, t).map((item) => item.title))
  return blocks
})

const trigger = ref<HTMLElement>()
const tipOpen = ref(false)
const tipStyle = ref<Record<string, string>>({})

function openTip() {
  const rect = trigger.value?.getBoundingClientRect()
  if (!details.value.length || !rect) return
  const left = `${Math.max(8, Math.min(rect.left, window.innerWidth - TIP_WIDTH - 8))}px`
  // 下方放不下时放到上方。
  tipStyle.value = window.innerHeight - rect.bottom < 240
    ? { left, top: `${rect.top - TIP_GAP}px`, transform: 'translateY(-100%)' }
    : { left, top: `${rect.bottom + TIP_GAP}px` }
  tipOpen.value = true
  window.addEventListener('scroll', closeTip, true)
}

function closeTip() {
  tipOpen.value = false
  window.removeEventListener('scroll', closeTip, true)
}

onBeforeUnmount(closeTip)
</script>
