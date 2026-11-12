<template>
  <Teleport to="body">
    <!-- appear：按需挂载（首次打开才渲染）的弹窗也播放进场动画 -->
    <Transition name="modal" appear>
      <div
        v-if="show"
        ref="rootRef"
        class="modal-overlay"
        :style="{ zIndex: stackZ }"
        :aria-labelledby="dialogId"
        role="dialog"
        aria-modal="true"
        data-base-dialog
        @keydown.tab="trapTab($event, dialogRef)"
        @click.self="handleClose"
      >
        <!-- Modal panel -->
        <div ref="dialogRef" :class="['modal-content', widthClasses]" tabindex="-1" @click.stop>
          <!-- Header -->
          <div class="modal-header">
            <h3 :id="dialogId" class="modal-title flex min-w-0 items-center gap-1.5">
              <slot name="title">{{ title }}</slot>
            </h3>
            <div v-if="$slots['header-actions']" class="ml-auto mr-2 flex items-center gap-2">
              <slot name="header-actions"></slot>
            </div>
            <button
              v-if="showCloseButton"
              @click="emit('close')"
              class="-mr-2 rounded-xl p-2 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-500/30 focus-visible:ring-offset-2 dark:text-dark-500 dark:hover:bg-dark-700 dark:hover:text-dark-300 dark:focus-visible:ring-offset-dark-900"
              aria-label="Close modal"
            >
              <Icon name="x" size="md" />
            </button>
          </div>

          <!-- Body -->
          <div ref="modalBodyRef" class="modal-body">
            <slot></slot>
          </div>

          <!-- Footer -->
          <div v-if="$slots.footer" class="modal-footer">
            <slot name="footer"></slot>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script lang="ts">
let dialogIdCounter = 0
</script>

<script setup lang="ts">
import { computed, watch, onMounted, onUnmounted, ref, nextTick, useId } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import { closeOverlay, holdFocus, isTopOverlay, openOverlay, syncOverlays, trapTab } from '@/utils/overlayStack'

// 生成唯一ID以避免多个对话框时ID冲突
const dialogId = `modal-title-${useId()}-${++dialogIdCounter}`
const dialogToken = Symbol('dialog')

// 焦点与叠放按 utils/overlayStack：打开时聚焦面板本身，Tab / Shift+Tab 留在层内，下层与页面 inert，
// 关闭时焦点交给仍可见的打开者。
const rootRef = ref<HTMLElement | null>(null)
const dialogRef = ref<HTMLElement | null>(null)
const modalBodyRef = ref<HTMLElement | null>(null)

type DialogWidth = 'narrow' | 'normal' | 'wide' | 'extra-wide' | 'full'

interface Props {
  show: boolean
  title: string
  width?: DialogWidth
  closeOnEscape?: boolean
  closeOnClickOutside?: boolean
  showCloseButton?: boolean
  /** Lowest z-index for this dialog; a dialog is always drawn above the layers opened before it. */
  zIndex?: number
  /** Changing it while open brings the dialog back to the top, with the focused element as its new opener. */
  raiseKey?: number
}

interface Emits {
  (e: 'close'): void
}

const props = withDefaults(defineProps<Props>(), {
  width: 'normal',
  closeOnEscape: true,
  closeOnClickOutside: false,
  showCloseButton: true,
  zIndex: undefined,
  raiseKey: 0
})

const emit = defineEmits<Emits>()

// 层级由打开顺序决定（utils/overlayStack），后打开的在上
const stackZ = ref<number>()

const widthClasses = computed(() => {
  // Width guidance: narrow=confirm/short prompts, normal=standard forms,
  // wide=multi-section forms or rich content, extra-wide=analytics/tables,
  // full=full-screen or very dense layouts.
  const widths: Record<DialogWidth, string> = {
    narrow: 'max-w-md',
    normal: 'max-w-lg',
    wide: 'w-full sm:max-w-2xl md:max-w-3xl lg:max-w-4xl',
    'extra-wide': 'w-full sm:max-w-3xl md:max-w-4xl lg:max-w-5xl xl:max-w-6xl',
    full: 'w-full sm:max-w-4xl md:max-w-5xl lg:max-w-6xl xl:max-w-7xl'
  }
  return widths[props.width]
})

const handleClose = () => {
  if (props.closeOnClickOutside) {
    emit('close')
  }
}

const handleEscape = (event: KeyboardEvent) => {
  if (!event.defaultPrevented && props.show && props.closeOnEscape && event.key === 'Escape' && isTopOverlay(dialogToken)) {
    event.preventDefault()
    emit('close')
  }
}

async function open() {
  stackZ.value = openOverlay(dialogToken, { floor: props.zIndex, root: () => rootRef.value, panel: () => dialogRef.value })
  await nextTick()
  if (!props.show) return
  syncOverlays()
  if (modalBodyRef.value) modalBodyRef.value.scrollTop = 0
  dialogRef.value?.focus()
}

watch(
  () => props.show,
  (isOpen) => {
    if (isOpen) void open()
    else closeOverlay(dialogToken)
  },
  { immediate: true }
)

watch(() => props.raiseKey, () => {
  if (props.show) void open()
})

onMounted(() => {
  document.addEventListener('keydown', handleEscape)
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleEscape)
  closeOverlay(dialogToken)
})

defineExpose({ holdFocus: () => holdFocus(dialogToken) })
</script>
