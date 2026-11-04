<template>
  <!-- 门槛小标识在最前，各行的标识上下对齐，扫一眼就能看出哪些账号需要关注 -->
  <div class="flex max-w-64 items-start gap-1.5">
    <button
      v-if="panel"
      ref="trigger"
      type="button"
      data-testid="group-quality-indicator"
      :data-state="panel.indicator"
      class="mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded transition-colors hover:bg-gray-100 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/40 dark:hover:bg-dark-700"
      :class="GROUP_QUALITY_INDICATOR_CLASSES[panel.indicator]"
      :aria-label="indicatorLabel"
      :aria-describedby="tooltipId"
      @mouseenter="scheduleOpen"
      @mouseleave="closeTip"
      @focus="openOnKeyboardFocus"
      @blur="closeTip"
      @click="openTip"
      @keydown.esc="closeTip"
    >
      <Icon :name="INDICATOR_ICONS[panel.indicator]" size="sm" class="h-3.5 w-3.5" aria-hidden="true" />
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
          <GroupQualityBadge
            v-if="statusBadgeOf(group.id)"
            :name="group.name"
            :quality="statusBadgeOf(group.id)!"
            :title="statusBadgeOf(group.id)!.kind === 'unknown' ? t('admin.groupQualityGate.unknownBadgeTitle', { group: group.name }) : group.name"
          />
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
      class="pointer-events-none fixed z-50 w-[22rem] rounded-lg bg-gray-900 p-3 text-xs leading-relaxed text-gray-100 shadow-lg dark:bg-dark-800"
      :style="tipStyle"
    >
      <div
        v-for="(block, index) in panel.blocks"
        :key="index"
        data-testid="quality-panel-block"
        :data-state="block.kind"
        :class="index ? 'mt-2.5 border-t border-white/10 pt-2.5' : undefined"
      >
        <p class="flex items-baseline justify-between gap-2">
          <span class="min-w-0 font-medium text-white">{{ block.title }}</span>
          <span v-if="block.strict" class="shrink-0 rounded bg-white/10 px-1.5 text-[11px] leading-4 text-gray-300">{{ t('admin.groupQualityGate.tooltip.strict') }}</span>
        </p>
        <p v-if="block.note" class="text-gray-400">{{ block.note }}</p>
        <p
          v-if="block.composition"
          data-testid="quality-panel-composition"
          :data-insufficient="block.composition.insufficient || undefined"
          :class="block.composition.insufficient ? 'text-amber-300' : 'text-gray-400'"
        >{{ block.composition.text }}</p>
        <table v-if="block.rows.length" class="mt-1.5 w-full tabular-nums">
          <thead>
            <tr class="text-[11px] text-gray-400">
              <th scope="col" class="pb-0.5 text-left font-normal">{{ t('admin.groupQualityGate.tooltip.dimension') }}</th>
              <th scope="col" class="pb-0.5 pl-2 text-left font-normal">{{ t('admin.groupQualityGate.tooltip.stateColumn') }}</th>
              <th scope="col" class="pb-0.5 pl-2 text-right font-normal">{{ t('admin.groupQualityGate.tooltip.measured') }}</th>
              <th scope="col" class="pb-0.5 pl-2 text-right font-normal">{{ t('admin.groupQualityGate.tooltip.samples') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in block.rows" :key="row.key" :data-metric="row.key" :data-state="row.state" class="align-top">
              <td class="whitespace-nowrap pb-1 text-gray-300">
                {{ row.label }}
                <span class="block text-[11px] leading-4 text-gray-400">{{ t('admin.groupQualityGate.tooltip.limitValue', { limit: row.limit }) }}</span>
              </td>
              <td class="whitespace-nowrap pl-2" :class="DIMENSION_STATE_CLASSES[row.state]">{{ row.stateLabel }}</td>
              <td class="pl-2 text-right" :class="row.state === 'fail' ? 'font-semibold text-red-300' : 'text-gray-100'">
                {{ row.value }}
                <span v-if="row.detail" class="block whitespace-nowrap text-[11px] font-normal leading-4 text-gray-400">{{ row.detail }}</span>
              </td>
              <td class="whitespace-nowrap pl-2 text-right text-gray-400">{{ row.samples }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="block.badMemory" data-testid="quality-panel-bad-memory" class="mt-1 text-amber-300">{{ block.badMemory }}</p>
      </div>
      <div v-if="panel.latencyHold" data-testid="quality-panel-latency-hold" class="mt-2.5 border-t border-white/10 pt-2">
        <p class="flex items-center gap-1 text-amber-300">
          <Icon name="clock" size="xs" class="shrink-0" aria-hidden="true" />{{ panel.latencyHold.text }}
        </p>
        <p class="text-gray-400">{{ panel.latencyHold.note }}</p>
      </div>
      <p v-if="panel.stale" class="mt-2 text-amber-300">{{ panel.stale }}</p>
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
import type { GroupQualityDimensionState } from '@/types/groupQualityGate'
import {
  GROUP_QUALITY_INDICATOR_CLASSES,
  QUALITY_CLOCK,
  groupQualityPanel,
  isGroupQualityAttention,
  orderGroupsByQuality,
  qualityTooltipTop,
  type GroupQualityCell,
  type GroupQualityIndicatorKind
} from './groupQualityPresentation'

// 账号列表的分组单元格。分组标签是一个按钮，点开“编辑分组”；没有分组时显示“—”也能点。
// 不达标、待复测与未知的分组用带状态的标签（未知为灰色），达标、固定与未开门槛的组显示普通标签。开启门槛时标签前有一个小标识，
// 只有悬停、键盘聚焦或点按它才弹出门槛面板：每个门槛分组一段（状态、本组构成、三维小表、坏记忆），底部是判慢让位。
const TIP_WIDTH = 352
const TIP_GAP = 6
const HOVER_DELAY_MS = 120

const INDICATOR_ICONS = {
  failing: 'exclamationTriangle',
  retest: 'exclamationTriangle',
  hold: 'clock',
  none: 'shield'
} as const satisfies Record<GroupQualityIndicatorKind, string>

const DIMENSION_STATE_CLASSES: Record<GroupQualityDimensionState, string> = {
  pass: 'text-emerald-300',
  fail: 'font-semibold text-red-300',
  none: 'text-gray-400'
}

const props = defineProps<{
  groups: Group[]
  accountName: string
  /** 账号平台；分组平台与之相同时不再重复显示平台图标 */
  accountPlatform?: string
  /** groupId → 该账号在开启门槛的分组里的状态 */
  quality?: Map<number, GroupQualityCell>
}>()

const emit = defineEmits<{
  open: []
}>()

const { t } = useI18n()
const tooltipId = useId()
const clock = (iso: string) => formatDateTime(iso, QUALITY_CLOCK)

const orderedGroups = computed(() => orderGroupsByQuality(props.groups, props.quality))
// 带状态的标签：不达标、待复测，以及门槛组里的未知（灰色，不用平台色，免得看起来像达标）。
const statusBadgeOf = (groupId: number) => {
  const cell = props.quality?.get(groupId)
  return cell && (isGroupQualityAttention(cell) || cell.kind === 'unknown') ? cell : undefined
}
const panel = computed(() => (props.quality?.size ? groupQualityPanel(orderedGroups.value, props.quality, t, clock) : null))
const indicatorLabel = computed(() => {
  const current = panel.value
  if (!current) return ''
  if (current.attentionCount) return t('admin.groupQualityGate.indicator.attention', { count: current.attentionCount })
  return t(current.indicator === 'hold' ? 'admin.groupQualityGate.indicator.hold' : 'admin.groupQualityGate.indicator.ok')
})

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
