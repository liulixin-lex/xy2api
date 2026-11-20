<template>
  <div class="usage-fit sq-focus" data-test="usage-fit" v-tip="allTip" :tabindex="windows.length ? 0 : undefined" @click="reveal">
    <div class="usage-fit__inner"><slot /></div>
    <span v-if="foldedCount" class="usage-fit__more" data-test="usage-more">{{ t('admin.accounts.table.usageMore', { n: foldedCount }) }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The usage windows column inside the accounts table's fixed 78px row (7e-6, 7g-3). Each window keeps to a single
 * line (its request / token / cost chips go to the tip), so the count decides: up to four windows all show; with more,
 * the first three show and "+N" counts the rest. Hovering the cell lists every window with its chips; so does a tap or
 * click on it, or keyboard focus (touch has no hover; mobile list plan). Its text is 12px, whatever size the windows
 * set for themselves elsewhere.
 *
 * The windows (UsageProgressBar) sign in through USAGE_WINDOW_FOLD as they mount and leave as they unmount; nothing is
 * measured or observed.
 */
import { computed, provide, shallowReactive, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { USAGE_WINDOW_FOLD } from '@/components/account/UsageProgressBar.vue'
import { showTipNow, vTip } from '@/utils/tip'

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

/** A window's texts (label, percentage, reset time, chips) with " · " between them, not run together (7i-7). */
function windowText(el: HTMLElement): string {
  const parts: string[] = []
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT)
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const text = (node.textContent ?? '').replace(/\s+/g, ' ').trim()
    if (text) parts.push(text)
  }
  return parts.join(' · ')
}
const allTip = () => windows.value.map(windowText).filter(Boolean).join('\n')
/** A tap or click shows every window at once; one on a control inside (a refresh) does that control's job only. */
const reveal = (event: MouseEvent) => {
  if (!windows.value.length) return
  if (event.target instanceof Element && event.target.closest('button, a, input')) return
  showTipNow(event.currentTarget)
}
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
  font-size: var(--font-size-small);
  line-height: 15px;
}
/* No text under 12px in the accounts table (mobile list plan): the windows' own 9–11px sizes are raised here only. */
.usage-fit__inner :deep([class*='text-[9px]']),
.usage-fit__inner :deep([class*='text-[10px]']),
.usage-fit__inner :deep([class*='text-[11px]']) {
  font-size: var(--font-size-small);
}
/* At 12px a window keeps to one line: its 32px label and percentage grow instead of wrapping ("7d S", "100%"). */
.usage-fit__inner :deep([data-usage-window] .w-\[32px\]) {
  width: auto;
  min-width: 32px;
  white-space: nowrap;
}
.usage-fit__inner :deep([data-usage-window] span) {
  white-space: nowrap;
}
/* The cell takes keyboard focus for its tip; the ring is drawn inside, the column clips what lies outside it. */
.usage-fit.sq-focus:focus-visible {
  outline-offset: -2px;
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
  height: 16px;
  color: var(--color-text-secondary);
  font-size: var(--font-size-small);
  line-height: 16px;
}
</style>
