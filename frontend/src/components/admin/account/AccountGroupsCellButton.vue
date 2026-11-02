<template>
  <!-- 门槛小标识在最前，各行的标识上下对齐，扫一眼就能看出哪些账号需要关注 -->
  <div class="flex max-w-64 items-start gap-1.5">
    <button
      v-if="panel"
      ref="trigger"
      type="button"
      data-testid="group-quality-indicator"
      :data-state="panel.attention ?? 'none'"
      class="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
      :class="GROUP_QUALITY_INDICATOR_CLASSES[panel.attention ?? 'none']"
      :aria-label="panel.attentionCount ? t('admin.groupQualityGate.indicator.attention', { count: panel.attentionCount }) : t('admin.groupQualityGate.indicator.ok')"
      :aria-describedby="tooltipId"
      @mouseenter="scheduleOpen"
      @mouseleave="closeTip"
      @focus="openOnKeyboardFocus"
      @blur="closeTip"
      @click="openTip"
      @keydown.esc="closeTip"
    >
      <Icon :name="panel.attention ? 'exclamationTriangle' : 'shield'" size="sm" class="h-3.5 w-3.5" aria-hidden="true" />
    </button>
    <button
      type="button"
      data-testid="account-groups-cell"
      class="-mx-1 flex min-w-0 max-w-56 flex-wrap items-center gap-1 rounded-md px-1 py-0.5 text-left transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
      :aria-label="t('admin.groupCategories.editAccountGroupsAria', { name: accountName })"
      :title="groups.length ? groups.map((group) => group.name).join('、') : undefined"
      @click="emit('open')"
    >
      <template v-if="groups.length">
        <!-- “+N”跟最后一个标签放在一起，换行时不会单独落到一行 -->
        <span v-for="(group, index) in visibleGroups" :key="group.id" class="inline-flex min-w-0 max-w-full items-center gap-1">
          <GroupQualityBadge v-if="attentionOf(group.id)" :name="group.name" :quality="attentionOf(group.id)!" :title="group.name" />
          <GroupBadge
            v-else
            :name="group.name"
            :platform="group.platform"
            :show-platform-icon="group.platform !== accountPlatform"
            :subscription-type="group.subscription_type"
            :rate-multiplier="group.rate_multiplier"
            :show-rate="false"
            :title="group.name"
            class="max-w-40"
          />
          <span v-if="hiddenCount > 0 && index === visibleGroups.length - 1" class="whitespace-nowrap text-xs font-medium text-gray-500 dark:text-gray-400">+{{ hiddenCount }}</span>
        </span>
      </template>
      <span v-else class="text-sm text-gray-400 dark:text-dark-500">—</span>
    </button>
  </div>
  <Teleport to="body">
    <div
      v-if="panel"
      v-show="tipOpen"
      :id="tooltipId"
      ref="tip"
      role="tooltip"
      data-testid="account-groups-quality-tooltip"
      class="pointer-events-none fixed z-50 w-72 rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-gray-100 shadow-lg dark:bg-dark-800"
      :style="tipStyle"
    >
      <div v-for="(block, index) in panel.blocks" :key="index" data-testid="quality-panel-block" :class="index ? 'mt-2.5 border-t border-white/10 pt-2.5' : undefined">
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
        <p v-if="block.tier" data-testid="quality-panel-tier" class="mt-1 text-gray-400">{{ block.tier }}</p>
      </div>
      <div v-if="panel.eligible.length" data-testid="quality-panel-eligible" :class="panel.blocks.length ? 'mt-2.5 border-t border-white/10 pt-2.5' : undefined">
        <p class="font-medium text-white">{{ t('admin.groupQualityGate.panel.eligible', { count: panel.eligible.length }) }}</p>
        <!-- 每组一行：名称（固定、余量）后接灰色的档位与实时快档线 -->
        <p v-for="item in panel.eligible" :key="item.name" class="text-gray-300">
          <template v-if="item.pinned"><Icon name="lock" size="xs" class="mr-0.5 inline-block align-[-1px] text-gray-400" aria-hidden="true" /><span class="sr-only">{{ t('admin.groupQualityGate.pinnedMark') }}</span></template>{{ item.name }}<span v-if="item.headroom" class="ml-1 whitespace-nowrap" :class="item.headroom.tone === 'danger' ? 'text-red-300' : 'text-amber-300'">{{ item.headroom.text }}</span><span class="ml-1 text-gray-400">{{ item.tier }}</span>
        </p>
      </div>
      <p v-if="windowItems.length" data-testid="quality-panel-window" class="mt-2.5 flex flex-wrap gap-x-2 border-t border-white/10 pt-2 tabular-nums text-gray-400">
        <span>{{ t('admin.groupQualityGate.panel.window') }}</span>
        <span v-for="item in windowItems" :key="item.key" class="whitespace-nowrap" :class="item.alert ? 'font-semibold text-red-300' : 'text-gray-200'">{{ item.text }}</span>
      </p>
      <p v-if="panel.stale" class="mt-1 text-amber-300">{{ panel.stale }}</p>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import GroupQualityBadge from './GroupQualityBadge.vue'
import { formatDateTime } from '@/utils/format'
import { isReducedMotion } from '@/composables/useMotionPreference'
import type { Group } from '@/types'
import type { GroupQualityAccountMetrics } from '@/types/groupQualityGate'
import {
  GROUP_QUALITY_INDICATOR_CLASSES,
  QUALITY_CLOCK,
  accountQualityMetricItems,
  groupQualityPanel,
  isGroupQualityAttention,
  orderGroupsByQuality,
  qualityTooltipTop,
  strictestQualityLimits,
  type GroupQualityCell
} from './groupQualityPresentation'

// 账号列表的分组单元格。分组标签是一个按钮，点开“编辑分组”；没有分组时显示“—”也能点。
// 只有熔断、保底保留、观察中的分组用带状态的标签，合格组显示普通标签。开启门槛时标签后有一个小标识，
// 只有悬停、键盘聚焦或点按它才弹出门槛面板：需关注的分组逐组展开，合格组每组一行（含档位与实时快档线），底部是账号完整窗口指标。
const TIP_WIDTH = 288
const TIP_GAP = 6
const HOVER_DELAY_MS = 120

const props = defineProps<{
  groups: Group[]
  accountName: string
  /** 账号平台；分组平台与之相同时不再重复显示平台图标 */
  accountPlatform?: string
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

const orderedGroups = computed(() => orderGroupsByQuality(props.groups, props.quality))
const attentionOf = (groupId: number) => {
  const cell = props.quality?.get(groupId)
  return cell && isGroupQualityAttention(cell) ? cell : undefined
}
const panel = computed(() => (props.quality?.size ? groupQualityPanel(orderedGroups.value, props.quality, t, clock, props.metrics) : null))
const windowItems = computed(() => (props.metrics ? accountQualityMetricItems(props.metrics, t, strictestQualityLimits(props.quality)) : []))

// 最多显示 2 个标签，名称尽量完整，其余收进 +N（按钮 title 列出全部分组）；需关注的排在前面，不会被收进 +N。
const MAX_VISIBLE = 2
const visibleGroups = computed(() => orderedGroups.value.slice(0, MAX_VISIBLE))
const hiddenCount = computed(() => Math.max(0, props.groups.length - MAX_VISIBLE))

const trigger = ref<HTMLElement>()
const tip = ref<HTMLElement>()
const tipOpen = ref(false)
const tipStyle = ref<Record<string, string>>({})
let hoverTimer: ReturnType<typeof setTimeout> | undefined
let motion: gsap.Context | undefined

// 悬停稍作停顿再打开，鼠标划过表格时不会接连弹出。
function scheduleOpen() {
  clearTimeout(hoverTimer)
  hoverTimer = setTimeout(openTip, HOVER_DELAY_MS)
}

// 鼠标点按也会先触发 focus；只有键盘聚焦（:focus-visible）才立即打开，点按交给 click。
function openOnKeyboardFocus(event: FocusEvent) {
  const target = event.target as HTMLElement
  let keyboard = true
  try {
    keyboard = target.matches(':focus-visible')
  } catch {
    // 不支持 :focus-visible 的环境按键盘聚焦处理
  }
  if (keyboard) void openTip()
}

async function openTip() {
  clearTimeout(hoverTimer)
  const rect = trigger.value?.getBoundingClientRect()
  if (tipOpen.value || !panel.value || !rect) return
  const left = `${Math.max(8, Math.min(rect.left, window.innerWidth - TIP_WIDTH - 8))}px`
  // 先隐藏着渲染出来量高度，再按实际高度放到下方或上方，长面板不会超出视口。
  tipStyle.value = { left, top: `${rect.bottom + TIP_GAP}px`, visibility: 'hidden' }
  tipOpen.value = true
  window.addEventListener('scroll', closeTip, true)
  document.addEventListener('pointerdown', closeOnOutside, true)
  await nextTick()
  if (!tipOpen.value) return
  const top = qualityTooltipTop(rect, tip.value?.offsetHeight ?? 0, window.innerHeight, TIP_GAP)
  tipStyle.value = { left, top: `${top}px` }
  await nextTick()
  playEnter(top >= rect.bottom ? -4 : 4)
}

// 唯一的动效：面板从标识方向淡入 4px。减少动画（系统或站内设置）时直接显示；收起不做动画。
function playEnter(fromY: number) {
  const el = tip.value
  if (!el || !tipOpen.value || isReducedMotion()) return
  motion ??= gsap.context(() => {})
  motion.add(() => gsap.fromTo(el, { autoAlpha: 0, y: fromY }, { autoAlpha: 1, y: 0, duration: 0.16, ease: 'power2.out', clearProps: 'opacity,visibility,transform' }))
}

function closeOnOutside(event: PointerEvent) {
  if (!trigger.value?.contains(event.target as Node)) closeTip()
}

// 收起或卸载时还原进行中的动画和内联样式。
function stopMotion() {
  motion?.revert()
  motion = undefined
}

function closeTip() {
  clearTimeout(hoverTimer)
  if (!tipOpen.value) return
  stopMotion()
  tipOpen.value = false
  window.removeEventListener('scroll', closeTip, true)
  document.removeEventListener('pointerdown', closeOnOutside, true)
}

onBeforeUnmount(closeTip)
</script>
