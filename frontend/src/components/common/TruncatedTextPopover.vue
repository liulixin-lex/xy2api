<template>
  <span ref="rootRef" @mouseenter="open" @mouseleave="close">{{ text }}<Teleport to="body">
      <div
        v-if="popoverStyle"
        data-test="truncated-text-popover"
        role="tooltip"
        class="pointer-events-none fixed z-[100000030] whitespace-pre-line rounded-lg bg-gray-900 px-3 py-2 text-left text-xs leading-relaxed text-white shadow-xl ring-1 ring-white/10 [overflow-wrap:anywhere] dark:bg-gray-800"
        :style="popoverStyle"
      >
        {{ text }}
      </div>
    </Teleport></span>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'

// 文字被截断时，悬停弹出完整内容，移开即关；弹层挂到 body，层级高于下拉面板。
// 模板保持单一根节点，调用方的 class（含 line-clamp）落在文字上。

const props = withDefaults(defineProps<{
  text: string
  enabled?: boolean
}>(), {
  enabled: false
})

const POPOVER_GAP = 6
const rootRef = ref<HTMLElement | null>(null)
const popoverStyle = ref<Record<string, string> | null>(null)

function open() {
  const el = rootRef.value
  if (!props.enabled || !el || el.scrollHeight <= el.clientHeight + 1) return
  const rect = el.getBoundingClientRect()
  const style: Record<string, string> = { left: `${rect.left}px`, width: `${rect.width}px` }
  if (rect.top > window.innerHeight / 2) {
    style.bottom = `${window.innerHeight - rect.top + POPOVER_GAP}px`
  } else {
    style.top = `${rect.bottom + POPOVER_GAP}px`
  }
  popoverStyle.value = style
  window.addEventListener('scroll', close, true)
}

function close() {
  popoverStyle.value = null
  window.removeEventListener('scroll', close, true)
}

onBeforeUnmount(close)
</script>
