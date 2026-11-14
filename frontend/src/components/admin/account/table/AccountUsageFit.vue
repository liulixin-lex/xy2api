<template>
  <div ref="box" class="usage-fit" data-test="usage-fit" v-tip="allTip">
    <div ref="inner" class="usage-fit__inner"><slot /></div>
    <span v-if="hiddenCount" class="usage-fit__more" data-test="usage-more">{{ t('admin.accounts.table.usageMore', { n: hiddenCount }) }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The usage windows column inside the accounts table's fixed 78px row (7e-6). The windows sit closer together and
 * each one keeps to a single line (its request / token / cost chips go to the tip); when they still do not fit, the
 * last windows are folded and "+N" counts them. Hovering the cell lists every window with its chips.
 *
 * A window is any element marked data-usage-window (UsageProgressBar marks its root). Fitting measures the rendered
 * cell, so it runs after each change of the content and of the cell's size.
 */
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { vTip } from '@/utils/tip'

const HIDDEN_ATTR = 'data-usage-folded'
/** Height of the "+N" line, kept free once something is folded. */
const MORE_LINE_PX = 14

const { t } = useI18n()
const box = ref<HTMLElement | null>(null)
const inner = ref<HTMLElement | null>(null)
const hiddenCount = ref(0)

const windows = () => Array.from(inner.value?.querySelectorAll<HTMLElement>('[data-usage-window]') ?? [])
const overflows = (room: number) => (inner.value?.scrollHeight ?? 0) > room

/** Shows every window, then folds from the last one until the rest fit with room for "+N". */
function fit() {
  const all = windows()
  all.forEach((el) => el.removeAttribute(HIDDEN_ATTR))
  const height = box.value?.clientHeight ?? 0
  let folded = 0
  if (height > 0 && overflows(height)) {
    for (let index = all.length - 1; index >= 0 && overflows(height - MORE_LINE_PX); index--) {
      all[index].setAttribute(HIDDEN_ATTR, '')
      folded++
    }
  }
  hiddenCount.value = folded
}

const allTip = () => windows().map((el) => (el.textContent ?? '').replace(/\s+/g, ' ').trim()).filter(Boolean).join('\n')

let frame = 0
const schedule = () => {
  cancelAnimationFrame(frame)
  frame = requestAnimationFrame(() => void nextTick(fit))
}
let mutations: MutationObserver | null = null
let resizes: ResizeObserver | null = null
onMounted(() => {
  fit()
  // Content changes (a usage answer, a window appearing) and size changes (the column, the row) fit again. Folding
  // only changes an attribute, which is not observed, so fitting never triggers itself.
  if (inner.value && typeof MutationObserver !== 'undefined') {
    mutations = new MutationObserver(schedule)
    mutations.observe(inner.value, { childList: true, subtree: true, characterData: true })
  }
  if (box.value && typeof ResizeObserver !== 'undefined') {
    resizes = new ResizeObserver(schedule)
    resizes.observe(box.value)
  }
})
onBeforeUnmount(() => {
  cancelAnimationFrame(frame)
  mutations?.disconnect()
  resizes?.disconnect()
})

defineExpose({ fit })
</script>

<style scoped>
.usage-fit {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
  justify-content: center;
  overflow: hidden;
}
.usage-fit__inner {
  min-height: 0;
  line-height: 15px;
}
/* Compact: one line per window, small gaps, no chips (they are in the tip). */
.usage-fit__inner :deep(.space-y-1 > :not([hidden]) ~ :not([hidden])),
.usage-fit__inner :deep(.space-y-1\.5 > :not([hidden]) ~ :not([hidden])) {
  margin-top: 2px;
}
.usage-fit__inner :deep(.leading-\[18px\]) {
  line-height: 15px;
}
.usage-fit__inner :deep([data-usage-window-stats]) {
  display: none;
}
.usage-fit__inner :deep(.mt-1) {
  margin-top: 2px;
}
.usage-fit__inner :deep(.mb-1) {
  margin-bottom: 2px;
}
.usage-fit__inner :deep([data-usage-folded]) {
  display: none;
}
.usage-fit__more {
  flex: none;
  height: 14px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-micro);
  line-height: 14px;
}
</style>
