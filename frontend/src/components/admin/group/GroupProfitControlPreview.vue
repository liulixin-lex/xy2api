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
    <template v-else-if="preview">
      <p
        ref="line"
        data-testid="profit-control-preview-line"
        :data-level="level"
        :class="[LEVEL_CLASSES[level], { 'opacity-60': loading }]"
      >
        <template v-if="level === 'empty'">
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
      <p v-if="level === 'danger'" class="mt-1 text-red-700 dark:text-red-400">
        {{ t('admin.groups.profitControl.previewZeroHint') }}
      </p>
    </template>
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
import type { GroupProfitControlPreview } from '@/types/groupQualityGate'
import {
  formatProfitCeiling,
  profitPercentToDecimal,
  profitPreviewLevel,
  type ProfitPreviewLevel
} from '@/views/admin/groupsProfitControl'

// 编辑现有分组时的利润门预览：毛利率换算出的上游倍率上限，以及可调度成员中能通过的数量。
// 改毛利率时防抖重新预览；保存前由父组件调用 blocksAllMembers() 取与当前毛利率一致的结果。
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
}>()

const { t } = useI18n()

const root = ref<HTMLElement>()
const line = ref<HTMLElement>()
const playMotion = useStateMotion(root)

const preview = shallowRef<GroupProfitControlPreview | null>(null)
const loading = ref(false)
const errorMessage = ref('')
let previewMargin: number | null = null
let controller: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | undefined
let seq = 0

// 与提交校验一致：0 ≤ 毛利率 < 100%，否则不预览。
const marginDecimal = computed(() => {
  const percent = Number(props.marginPercent || 0)
  if (!Number.isFinite(percent) || percent < 0) return null
  const decimal = profitPercentToDecimal(percent)
  return decimal < 1 ? decimal : null
})

const level = computed(() => (preview.value ? profitPreviewLevel(preview.value) : 'normal'))

async function fetchPreview(margin: number) {
  clearTimeout(timer)
  controller?.abort()
  const current = (controller = new AbortController())
  const request = ++seq
  loading.value = true
  try {
    const result = await adminAPI.groups.getProfitControlPreview(props.groupId, margin, { signal: current.signal })
    if (request !== seq) return
    preview.value = result
    previewMargin = margin
    errorMessage.value = ''
  } catch (error) {
    if (request !== seq) return
    preview.value = null
    previewMargin = null
    errorMessage.value = extractApiErrorMessage(error, t('common.unknownError'))
  } finally {
    if (request === seq) loading.value = false
  }
}

watch(marginDecimal, (margin) => {
  clearTimeout(timer)
  if (margin === null) return
  timer = setTimeout(() => void fetchPreview(margin), PREVIEW_DEBOUNCE_MS)
})

// 数值换了一次，轻推一下让人看见它刷新过。
watch(preview, async (next, previous) => {
  if (!next || !previous) return
  if (next.passing === previous.passing && next.ceiling === previous.ceiling && next.schedulable === previous.schedulable) return
  await nextTick()
  const el = line.value
  if (el) playMotion(() => gsap.fromTo(el, { autoAlpha: 0.4, y: 3 }, { autoAlpha: 1, y: 0, duration: 0.2, ease: 'expo.out', clearProps: 'opacity,visibility,transform' }))
})

onMounted(() => {
  if (marginDecimal.value !== null) void fetchPreview(marginDecimal.value)
})

onUnmounted(() => {
  clearTimeout(timer)
  controller?.abort()
})

/** 保存前调用：取与当前毛利率一致的预览，返回是否有可调度成员却没有一个能通过。预览失败不拦截保存。 */
async function blocksAllMembers(): Promise<boolean> {
  const margin = marginDecimal.value
  if (margin === null) return false
  if (previewMargin !== margin || loading.value) await fetchPreview(margin)
  return previewMargin === margin && !!preview.value && profitPreviewLevel(preview.value) === 'danger'
}

defineExpose({ blocksAllMembers })
</script>
