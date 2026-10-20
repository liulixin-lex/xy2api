<template>
  <span class="tabular-nums">{{ display }}</span>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { isReducedMotion } from '@/composables/useMotionPreference'

/**
 * 首次显示时从 0 滚动到目标值（640ms，easeOutExpo）。
 * 之后数值变化（刷新、轮询）直接显示新值，不再滚动；减少动画时也直接显示。
 */
const props = withDefaults(
  defineProps<{
    value: number
    format?: (value: number) => string
    duration?: number
  }>(),
  { duration: 640 }
)

const formatValue = (value: number) => (props.format ? props.format(value) : String(value))
const shouldAnimate =
  Number.isFinite(props.value) && props.value !== 0 && !isReducedMotion() && typeof requestAnimationFrame !== 'undefined'
const display = ref(formatValue(shouldAnimate ? 0 : props.value))
let frame = 0

const easeOutExpo = (t: number) => (t >= 1 ? 1 : 1 - Math.pow(2, -10 * t))

onMounted(() => {
  if (!shouldAnimate) return
  const target = props.value
  const integer = Number.isInteger(target)
  const start = performance.now()
  const step = (now: number) => {
    const progress = Math.min(1, (now - start) / props.duration)
    const current = target * easeOutExpo(progress)
    display.value = formatValue(progress >= 1 ? target : integer ? Math.round(current) : current)
    if (progress < 1) frame = requestAnimationFrame(step)
  }
  frame = requestAnimationFrame(step)
})

watch(
  () => [props.value, props.format] as const,
  () => {
    if (frame) cancelAnimationFrame(frame)
    frame = 0
    display.value = formatValue(props.value)
  }
)

onBeforeUnmount(() => {
  if (frame) cancelAnimationFrame(frame)
})
</script>
