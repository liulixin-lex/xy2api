import { onBeforeUnmount, ref, watch, type Ref } from 'vue'
import { isReducedMotion } from '@/composables/useMotionPreference'

const DURATION_MS = 260

/**
 * A display copy of `source` that glides to each new value (ease-out cubic) instead of jumping, so the amount details
 * read as one number changing rather than a flicker of digits. Display only: callers keep using the exact source value
 * for anything they submit. A zero is where the figure leaves the screen, so it keeps the last figure while it folds
 * away, and the next figure after a zero appears as it is. Jumps straight to the value when reduced motion is on or
 * no frame clock exists.
 */
export function useTweenedNumber(source: () => number, duration = DURATION_MS): Ref<number> {
  const shown = ref(source())
  let frame = 0
  let held = shown.value === 0

  function stop() {
    if (frame) cancelAnimationFrame(frame)
    frame = 0
  }

  watch(source, (to) => {
    stop()
    if (to === 0) {
      held = true
      return
    }
    const from = shown.value
    if (held || from === to || typeof requestAnimationFrame !== 'function' || isReducedMotion()) {
      held = false
      shown.value = to
      return
    }
    const start = performance.now()
    const step = (now: number) => {
      const progress = Math.min(1, (now - start) / duration)
      shown.value = progress >= 1 ? to : from + (to - from) * (1 - (1 - progress) ** 3)
      frame = progress >= 1 ? 0 : requestAnimationFrame(step)
    }
    frame = requestAnimationFrame(step)
  })

  onBeforeUnmount(stop)
  return shown
}
