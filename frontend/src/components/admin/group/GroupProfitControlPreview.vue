<template>
  <div
    ref="root"
    data-testid="profit-control-preview"
    class="rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-700"
    aria-live="polite"
    :aria-busy="loading"
  >
    <p v-if="marginDecimal === null" class="text-gray-500 dark:text-gray-400">
      {{ t('admin.groups.profitControl.previewInvalid') }}
    </p>
    <p v-else-if="floorValue === null" class="text-gray-500 dark:text-gray-400" data-testid="profit-control-preview-floor-invalid">
      {{ t('admin.groups.profitControl.floorPreviewInvalid') }}
    </p>
    <div v-else-if="preview" ref="line" class="space-y-1" :class="{ 'opacity-60': loading }">
      <p
        data-testid="profit-control-preview-line"
        :data-level="level"
        :class="LEVEL_CLASSES[level]"
      >
        <template v-if="!profitEnabled">
          {{ t('admin.groups.profitControl.floorPreviewOnly', {
            floor: formatProfitCeiling(preview.floor),
            schedulable: preview.schedulable,
            below: preview.below_floor
          }) }}
        </template>
        <template v-else-if="preview.range_empty">
          {{ t('admin.groups.profitControl.floorRangeEmpty', {
            floor: formatProfitCeiling(preview.floor),
            ceiling: formatProfitCeiling(preview.ceiling)
          }) }}
        </template>
        <template v-else-if="level === 'empty'">
          {{ t('admin.groups.profitControl.previewEmpty', { ceiling: formatProfitCeiling(preview.ceiling) }) }}
        </template>
        <template v-else>
          {{ t('admin.groups.profitControl.preview', {
            ceiling: formatProfitCeiling(preview.ceiling),
            schedulable: preview.schedulable,
            passing: preview.passing
          }) }}
        </template>
      </p>
      <p
        v-if="profitEnabled && preview.floor > 0 && !preview.range_empty"
        class="text-gray-600 dark:text-gray-300"
        data-testid="profit-control-preview-below-floor"
      >
        {{ t('admin.groups.profitControl.floorBelow', { floor: formatProfitCeiling(preview.floor), n: preview.below_floor }, preview.below_floor) }}
      </p>
      <p v-if="preview.pinned > 0" class="text-gray-600 dark:text-gray-300" data-testid="profit-control-preview-pinned">
        {{ t('admin.groups.profitControl.pinnedCount', { n: preview.pinned }, preview.pinned) }}
      </p>
      <p
        v-if="profitEnabled && preview.pinned_loss.length"
        class="font-medium text-amber-700 dark:text-amber-400"
        data-testid="profit-control-preview-pinned-loss"
      >
        {{ t('admin.groups.profitControl.pinnedLoss', {
          ceiling: formatProfitCeiling(preview.ceiling),
          names: preview.pinned_loss
            .map((item) => t('admin.groups.profitControl.pinnedLossItem', { name: item.name, rate: formatProfitCeiling(item.rate) }))
            .join(t('admin.groups.profitControl.pinnedLossJoin'))
        }) }}
      </p>
    </div>
    <p v-else-if="errorMessage" class="text-gray-500 dark:text-gray-400">
      {{ t('admin.groups.profitControl.previewFailed', { message: errorMessage }) }}
    </p>
    <p v-else class="text-gray-500 dark:text-gray-400">
      {{ t('admin.groups.profitControl.previewLoading') }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { gsap } from 'gsap'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useStateMotion } from '@/composables/useStateMotion'
import type { GroupProfitControlPreview } from '@/types/groupProfitControl'
import {
  effectiveProfitMarginPercent,
  floorOnlyPreviewLevel,
  formatProfitCeiling,
  minUpstreamRateValue,
  profitBlockReason,
  profitPercentToDecimal,
  profitPreviewLevel,
  validateMinUpstreamRate,
  type MinUpstreamRateInput,
  type ProfitBlockReason,
  type ProfitPreviewLevel
} from '@/views/admin/groupsProfitControl'

// 编辑现有分组时的利润门预览：毛利率换算出的上游倍率上限、可调度成员中能通过的数量，以及最低上游倍率
// （倍率区间的下限）排除的号数、固定号数和高于上限的固定号。利润开关关闭、只设了下限时只显示与下限有关的几行。
// 改毛利率或下限时防抖重新预览；保存前由父组件调用 blockingReason() 取与当前输入一致的结果。
const PREVIEW_DEBOUNCE_MS = 400

const LEVEL_CLASSES: Record<ProfitPreviewLevel, string> = {
  empty: 'text-gray-600 dark:text-gray-300',
  danger: 'font-medium text-red-700 dark:text-red-400',
  warning: 'font-medium text-amber-700 dark:text-amber-400',
  normal: 'text-gray-600 dark:text-gray-300'
}

const props = defineProps<{
  groupId: number
  marginPercent: number | string | null
  /** 利润开关；关闭时上限不生效，只预览下限。 */
  profitEnabled: boolean
  /** 最低上游倍率输入框的值；留空表示不设。 */
  floor: MinUpstreamRateInput
  /** 输入框里是读不出数的内容（value 为空但不是留空）：按无效处理，不预览。 */
  floorUnreadable?: boolean
}>()

const { t } = useI18n()

const root = ref<HTMLElement>()
const line = ref<HTMLElement>()
const playMotion = useStateMotion(root)

const preview = shallowRef<GroupProfitControlPreview | null>(null)
const loading = ref(false)
const errorMessage = ref('')
let previewKey: string | null = null
let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let seq = 0

// 与提交校验一致：留空按默认 20%，0 ≤ 毛利率 < 100%，否则不预览。利润开关关闭时上限不生效，按 0 试算。
const marginDecimal = computed(() => {
  if (!props.profitEnabled) return 0
  const percent = effectiveProfitMarginPercent(props.marginPercent)
  if (!Number.isFinite(percent) || percent < 0) return null
  const decimal = profitPercentToDecimal(percent)
  return decimal < 1 ? decimal : null
})

// 下限：留空为 0（不设），填错时不预览。
const floorValue = computed(() =>
  validateMinUpstreamRate(props.floor, props.floorUnreadable) === null ? (minUpstreamRateValue(props.floor) ?? 0) : null
)

const query = computed(() =>
  marginDecimal.value === null || floorValue.value === null ? null : { margin: marginDecimal.value, floor: floorValue.value }
)
const keyOf = (q: { margin: number; floor: number }) => `${q.margin}:${q.floor}`

const level = computed<ProfitPreviewLevel>(() => {
  if (!preview.value) return 'normal'
  return props.profitEnabled ? profitPreviewLevel(preview.value) : floorOnlyPreviewLevel(preview.value)
})

async function fetchPreview(q: { margin: number; floor: number }) {
  clearTimeout(timer)
  controller?.abort()
  const current = (controller = new AbortController())
  const request = ++seq
  loading.value = true
  try {
    const result = await adminAPI.groups.getProfitControlPreview(props.groupId, q.margin, q.floor, { signal: current.signal })
    if (request !== seq) return
    preview.value = result
    previewKey = keyOf(q)
    errorMessage.value = ''
  } catch (error) {
    if (request !== seq) return
    preview.value = null
    previewKey = null
    errorMessage.value = extractApiErrorMessage(error, t('common.unknownError'))
  } finally {
    if (request === seq) loading.value = false
  }
}

watch(query, (q) => {
  clearTimeout(timer)
  if (q === null) return
  timer = setTimeout(() => void fetchPreview(q), PREVIEW_DEBOUNCE_MS)
})

// 数值换了一次，轻推一下让人看见它刷新过。
watch(preview, async (next, previous) => {
  if (!next || !previous) return
  if (
    next.passing === previous.passing && next.ceiling === previous.ceiling && next.schedulable === previous.schedulable &&
    next.below_floor === previous.below_floor && next.range_empty === previous.range_empty
  ) return
  await nextTick()
  const el = line.value
  if (el) playMotion(() => gsap.fromTo(el, { autoAlpha: 0.4, y: 3 }, { autoAlpha: 1, y: 0, duration: 0.2, ease: 'expo.out', clearProps: 'opacity,visibility,transform' }))
})

onMounted(() => {
  if (query.value !== null) void fetchPreview(query.value)
})

onUnmounted(() => {
  clearTimeout(timer)
  controller?.abort()
})

/**
 * 保存前调用：取与当前输入一致的预览，返回需要确认的原因（区间为空，或有可调度成员却没有一个能用），
 * 不需要确认时为 null。预览失败不拦截保存。
 */
async function blockingReason(): Promise<ProfitBlockReason | null> {
  const q = query.value
  if (q === null) return null
  if (previewKey !== keyOf(q) || loading.value) await fetchPreview(q)
  if (previewKey !== keyOf(q) || !preview.value) return null
  return profitBlockReason(preview.value, props.profitEnabled)
}

defineExpose({ blockingReason })
</script>
