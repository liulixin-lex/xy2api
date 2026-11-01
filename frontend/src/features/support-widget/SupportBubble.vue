<template>
  <button
    type="button"
    class="fixed left-0 top-0 z-[45] flex touch-none select-none items-center justify-center rounded-full bg-gradient-to-br from-primary-600 to-primary-700 text-white shadow-lg shadow-primary-700/30 hover:brightness-110 focus:outline-none focus-visible:ring-4 focus-visible:ring-primary-300 dark:from-primary-600 dark:to-primary-800 dark:shadow-primary-500/25 dark:ring-1 dark:ring-primary-300/40 dark:focus-visible:ring-4 dark:focus-visible:ring-primary-400/70"
    :class="bubble.dragging.value ? 'cursor-grabbing' : 'cursor-pointer'"
    :style="bubble.style.value"
    :aria-label="expanded ? t('supportChat.bubble.close') : t('supportChat.bubble.open')"
    :aria-expanded="expanded"
    data-testid="support-bubble"
    v-on="bubble.handlers"
    @click="onClick"
  >
    <Icon v-if="expanded" name="x" size="lg" />
    <span
      v-else
      class="flex flex-col items-center [text-shadow:0_1px_1px_rgb(0_0_0/0.18)]"
      aria-hidden="true"
      data-testid="support-bubble-mark"
    >
      <span class="text-[15px] font-extrabold leading-none tracking-tighter sm:text-[17px]">GG</span>
      <span class="mt-[3px] whitespace-nowrap text-[9px] font-semibold leading-none" data-testid="support-bubble-label">{{ t('supportChat.bubble.label') }}</span>
    </span>
    <span
      v-if="badge > 0"
      class="absolute -right-0.5 -top-0.5 inline-flex min-w-5 items-center justify-center rounded-full bg-red-600 px-1.5 text-[10px] font-semibold leading-5 text-white ring-2 ring-white dark:ring-dark-900"
      data-testid="support-bubble-badge"
    >{{ badge > 99 ? '99+' : badge }}</span>
  </button>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { useFloatingBubble } from './useFloatingBubble'

const props = defineProps<{ bubble: ReturnType<typeof useFloatingBubble>; expanded: boolean; badge: number }>()
const emit = defineEmits<{ (e: 'toggle'): void }>()
const { t } = useI18n()

function onClick(event: MouseEvent) {
  if (props.bubble.acceptClick(event)) emit('toggle')
}
</script>
