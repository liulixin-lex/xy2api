<template>
  <button
    type="button"
    class="fixed left-0 top-0 z-[45] flex touch-none select-none items-center justify-center rounded-full bg-primary-600 text-white shadow-lg shadow-primary-600/30 hover:bg-primary-700 focus:outline-none focus-visible:ring-4 focus-visible:ring-primary-300 dark:shadow-black/40"
    :class="bubble.dragging.value ? 'cursor-grabbing' : 'cursor-pointer'"
    :style="bubble.style.value"
    :aria-label="expanded ? t('supportChat.bubble.close') : t('supportChat.bubble.open')"
    :aria-expanded="expanded"
    data-testid="support-bubble"
    v-on="bubble.handlers"
    @click="onClick"
  >
    <Icon :name="expanded ? 'x' : 'chat'" size="lg" />
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
