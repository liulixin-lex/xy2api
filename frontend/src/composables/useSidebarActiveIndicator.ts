import { nextTick, onBeforeUnmount, onMounted, watch, type Ref, type WatchSource } from 'vue'
import { isReducedMotion } from './useMotionPreference'

// 侧栏随每个页面重新挂载（AppLayout 在各视图内），用模块级变量记住上一次指示条的位置，
// 新实例挂载时先停在旧位置，再滑到当前项，切页时看起来是同一根指示条在移动。
let lastPosition: { top: number; height: number } | null = null

/**
 * 侧栏当前项指示条：只改 transform（高度在移动前一次性写入），测量放在 rAF 里批量读取。
 */
export function useSidebarActiveIndicator(
  navRef: Ref<HTMLElement | null>,
  indicatorRef: Ref<HTMLElement | null>,
  sources: WatchSource[]
) {
  let frame = 0

  const apply = (top: number, height: number, instant: boolean) => {
    const indicator = indicatorRef.value
    if (!indicator) return
    indicator.dataset.instant = instant ? 'true' : 'false'
    indicator.style.height = `${height}px`
    indicator.style.transform = `translate3d(0, ${top}px, 0)`
    indicator.dataset.visible = 'true'
  }

  const place = (instant: boolean) => {
    const nav = navRef.value
    const indicator = indicatorRef.value
    if (!nav || !indicator) return
    const active = nav.querySelector<HTMLElement>('.sidebar-link-active')
    if (!active) {
      indicator.dataset.visible = 'false'
      return
    }
    const navRect = nav.getBoundingClientRect()
    const rect = active.getBoundingClientRect()
    const height = Math.round(rect.height * 0.5)
    const top = Math.round(rect.top - navRect.top + nav.scrollTop + (rect.height - height) / 2)
    apply(top, height, instant || isReducedMotion())
    lastPosition = { top, height }
  }

  const schedule = (instant = false) => {
    if (typeof requestAnimationFrame === 'undefined') return
    cancelAnimationFrame(frame)
    frame = requestAnimationFrame(() => place(instant))
  }

  onMounted(() => {
    if (lastPosition && !isReducedMotion()) {
      apply(lastPosition.top, lastPosition.height, true)
      // 让旧位置先提交一帧，再开启过渡滑向当前项
      if (typeof requestAnimationFrame !== 'undefined') {
        frame = requestAnimationFrame(() => schedule(false))
      }
    } else {
      schedule(true)
    }
  })

  watch(sources, () => {
    void nextTick(() => schedule(false))
  })

  onBeforeUnmount(() => {
    if (typeof cancelAnimationFrame !== 'undefined') cancelAnimationFrame(frame)
  })

  return { refresh: () => schedule(false) }
}
