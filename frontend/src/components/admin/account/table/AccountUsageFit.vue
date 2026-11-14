<template>
  <div class="usage-fit" data-test="usage-fit" v-tip="allTip">
    <div class="usage-fit__inner"><slot /></div>
    <span v-if="foldedCount" class="usage-fit__more" data-test="usage-more">{{ t('admin.accounts.table.usageMore', { n: foldedCount }) }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The usage windows column inside the accounts table's fixed 78px row (7e-6, 7g-3). Each window keeps to a single
 * line (its request / token / cost chips go to the tip), so the count decides: up to four windows all show; with more,
 * the first three show and "+N" counts the rest. Hovering the cell lists every window with its chips.
 *
 * The windows (UsageProgressBar) sign in through USAGE_WINDOW_FOLD as they mount and leave as they unmount; nothing is
 * measured or observed.
 */
import { computed, provide, shallowReactive, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { USAGE_WINDOW_FOLD } from '@/components/account/UsageProgressBar.vue'
import { vTip } from '@/utils/tip'

/** At most this many windows show in full; past it, SHOWN_WHEN_FOLDED show and "+N" stands for the rest. */
const MAX_SHOWN = 4
const SHOWN_WHEN_FOLDED = 3

const { t } = useI18n()
const roots = shallowReactive(new Set<Ref<HTMLElement | null>>())
/** The windows in the order they are on screen (one may mount after its neighbours). */
const windows = computed(() =>
  [...roots]
    .map((root) => root.value)
    .filter((el): el is HTMLElement => !!el)
    .sort((a, b) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1))
)
const foldedCount = computed(() => (windows.value.length > MAX_SHOWN ? windows.value.length - SHOWN_WHEN_FOLDED : 0))

provide(USAGE_WINDOW_FOLD, {
  add: (root) => roots.add(root),
  remove: (root) => roots.delete(root),
  folded: (root) => !!root && foldedCount.value > 0 && windows.value.indexOf(root) >= SHOWN_WHEN_FOLDED
})

const allTip = () => windows.value.map((el) => (el.textContent ?? '').replace(/\s+/g, ' ').trim()).filter(Boolean).join('\n')
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
