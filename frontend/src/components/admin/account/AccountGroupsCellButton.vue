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
      ref="tip"
      role="tooltip"
      data-testid="account-groups-quality-tooltip"
      class="pointer-events-none fixed z-50 w-72 rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-gray-100 shadow-lg dark:bg-dark-800"
      :style="tipStyle"
    >
      <div v-for="(block, index) in details" :key="index" :class="index ? 'mt-2.5 border-t border-white/10 pt-2.5' : undefined">
        <p class="font-medium text-white">{{ block.title }}</p>
        <p v-if="block.note" class="text-gray-400">{{ block.note }}</p>
        <div v-if="block.reasons.length" class="mt-1 flex flex-wrap gap-1">
          <span v-for="reason in block.reasons" :key="reason" class="rounded bg-red-500/20 px-1.5 text-[11px] leading-4 text-red-200">{{ reason }}</span>
        </div>
        <table v-if="block.rows.length" class="mt-1.5 w-full tabular-nums">
          <thead>
            <tr class="text-[11px] text-gray-400">
              <th scope="col" class="pb-0.5 text-left font-normal">{{ t('admin.groupQualityGate.tooltip.metric') }}</th>
              <th scope="col" class="pb-0.5 text-right font-normal">
                {{ t('admin.groupQualityGate.tooltip.measured') }}<span v-if="block.measuredNote" class="ml-1">{{ block.measuredNote }}</span>
              </th>
              <th scope="col" class="pb-0.5 pl-3 text-right font-normal">{{ t('admin.groupQualityGate.tooltip.limit') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in block.rows" :key="row.key" :data-metric="row.key" :data-over="row.over || undefined">
              <td class="pr-2 text-gray-300">{{ row.label }}</td>
              <td class="whitespace-nowrap text-right" :class="row.over ? 'font-semibold text-red-300' : 'text-gray-100'">
                {{ row.value }}<span v-if="row.note" class="ml-1 text-[11px] font-normal text-gray-400">{{ row.note }}</span>
              </td>
              <td class="whitespace-nowrap pl-3 text-right text-gray-300">{{ row.limit }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="block.headroom" class="mt-1" :class="block.headroom.tone === 'danger' ? 'text-red-300' : 'text-amber-300'">{{ block.headroom.text }}</p>
        <p v-if="block.stale" class="mt-1 text-amber-300">{{ block.stale }}</p>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from '@/components/common/GroupBadge.vue'
import GroupQualityBadge from './GroupQualityBadge.vue'
import { formatDateTime } from '@/utils/format'
import type { Group } from '@/types'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import {
  QUALITY_CLOCK,
  groupQualityTooltip,
  orderGroupsByQuality,
  qualityTooltipTop,
  type GroupQualityCell
} from './groupQualityPresentation'

// 账号列表的分组单元格：整格是一个按钮，点开“编辑分组”弹窗；没有分组时显示“—”也能点。
// 开启质量门槛的分组按状态着色，有问题的排在前面。门槛说明（状态、原因、实测与上限）
// 放在悬停和键盘聚焦都会出现的提示里，并作为按钮的补充描述给读屏。
const TIP_WIDTH = 288
const TIP_GAP = 6

const props = defineProps<{
  groups: Group[]
  accountName: string
  /** groupId → 该账号在开启门槛的分组里的状态 */
  quality?: Map<number, GroupQualityCell>
  /** 账号的完整窗口指标；熔断中的分组用它显示触发熔断的数据 */
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

// 每个门槛分组一段（含收进 +N 的）。
const details = computed(() =>
  orderedGroups.value
    .filter((group) => props.quality?.has(group.id))
    .map((group) => groupQualityTooltip(group.name, props.quality!.get(group.id)!, t, clock, props.metrics))
)

const trigger = ref<HTMLElement>()
const tip = ref<HTMLElement>()
const tipOpen = ref(false)
const tipStyle = ref<Record<string, string>>({})

async function openTip() {
  const rect = trigger.value?.getBoundingClientRect()
  if (!details.value.length || !rect) return
  const left = `${Math.max(8, Math.min(rect.left, window.innerWidth - TIP_WIDTH - 8))}px`
  // 先隐藏着渲染出来量高度，再按实际高度放到下方或上方，长提示不会超出视口。
  tipStyle.value = { left, top: `${rect.bottom + TIP_GAP}px`, visibility: 'hidden' }
  tipOpen.value = true
  window.addEventListener('scroll', closeTip, true)
  await nextTick()
  if (!tipOpen.value) return
  const top = qualityTooltipTop(rect, tip.value?.offsetHeight ?? 0, window.innerHeight, TIP_GAP)
  tipStyle.value = { left, top: `${top}px` }
}

function closeTip() {
  tipOpen.value = false
  window.removeEventListener('scroll', closeTip, true)
}

onBeforeUnmount(closeTip)
</script>
