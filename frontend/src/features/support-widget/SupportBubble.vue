<template>
  <button
    type="button"
    class="fixed left-0 top-0 z-[45] flex touch-none select-none items-center justify-center rounded-full bg-primary-700 text-white shadow-[0_6px_16px_rgb(15_23_42/0.18)] hover:bg-primary-800 focus:outline-none focus-visible:ring-4 focus-visible:ring-primary-300 dark:shadow-[0_6px_16px_rgb(0_0_0/0.5)] dark:ring-1 dark:ring-primary-300/35 dark:focus-visible:ring-4 dark:focus-visible:ring-primary-400/70"
    :class="bubble.dragging.value ? 'cursor-grabbing' : 'cursor-pointer'"
    :style="bubble.style.value"
    :aria-label="expanded ? t('supportChat.bubble.close') : t('supportChat.bubble.open')"
    :aria-expanded="expanded"
    data-testid="support-bubble"
    v-on="bubble.handlers"
    @click="onClick"
  >
    <span
      v-if="showHalo"
      class="support-bubble-halo pointer-events-none absolute -inset-[5px] rounded-full"
      aria-hidden="true"
      data-testid="support-bubble-halo"
    />
    <Icon v-if="expanded" name="x" size="lg" />
    <span
      v-else
      class="flex flex-col items-center"
      aria-hidden="true"
      data-testid="support-bubble-mark"
    >
      <span class="text-[18px] font-bold leading-none tracking-[0.02em] sm:text-[19px]">GG</span>
      <span class="mt-[3px] whitespace-nowrap text-[10px] font-medium leading-none sm:text-[11px]" data-testid="support-bubble-label">{{ t('supportChat.bubble.label') }}</span>
    </span>
    <span
      v-if="badge > 0"
      class="absolute -right-0.5 -top-0.5 inline-flex min-w-5 items-center justify-center rounded-full bg-red-600 px-1.5 text-[10px] font-semibold leading-5 text-white ring-2 ring-white dark:ring-dark-900"
      data-testid="support-bubble-badge"
    >{{ badge > 99 ? '99+' : badge }}</span>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useMotionPreference } from '@/composables/useMotionPreference'
import type { useFloatingBubble } from './useFloatingBubble'

const props = defineProps<{ bubble: ReturnType<typeof useFloatingBubble>; expanded: boolean; badge: number }>()
const emit = defineEmits<{ (e: 'toggle'): void }>()
const { t } = useI18n()
const { reducedMotion } = useMotionPreference()
const showHalo = computed(() => !props.expanded && !reducedMotion.value)

function onClick(event: MouseEvent) {
  if (props.bubble.acceptClick(event)) emit('toggle')
}
</script>

<style scoped>
/* 收起时外圈的细光环：一段渐亮的弧绕按钮转，3.2 秒一圈。
   只转 transform，由合成层完成、不逐帧重绘；开启“减少动画”时不渲染（见 docs/FRONTEND_UX.md）。 */
.support-bubble-halo {
  background: conic-gradient(
    rgb(20 184 166 / 0) 0deg,
    rgb(20 184 166 / 0.15) 120deg,
    rgb(20 184 166 / 0.9) 300deg,
    theme('colors.primary.300') 350deg,
    rgb(20 184 166 / 0) 360deg
  );
  /* 只留最外 2.5px，与按钮之间空出 2.5px */
  mask: radial-gradient(farthest-side, transparent calc(100% - 2.5px), #000 calc(100% - 2px));
  animation: support-bubble-halo-spin 3.2s linear infinite;
  will-change: transform;
}

:root.dark .support-bubble-halo {
  background: conic-gradient(
    rgb(45 212 191 / 0) 0deg,
    rgb(45 212 191 / 0.15) 120deg,
    rgb(45 212 191 / 0.9) 300deg,
    theme('colors.primary.100') 350deg,
    rgb(45 212 191 / 0) 360deg
  );
}

@keyframes support-bubble-halo-spin {
  to {
    transform: rotate(1turn);
  }
}
</style>
