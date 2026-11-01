import { onMounted, onUnmounted, type Ref } from 'vue'
import { gsap } from 'gsap'
import { isReducedMotion } from './useMotionPreference'

/**
 * 状态切换的小动效（数值刷新、区块展开）。系统要求减少动画时 matchMedia 不进入、并还原已播放的动画；
 * 用户在菜单里打开“减少动画”时也不播放。动画都挂在 scope 的上下文里，组件卸载时统一还原。
 */
export function useStateMotion(scope: Ref<HTMLElement | undefined>) {
  const mm = gsap.matchMedia()
  let motion: gsap.Context | undefined

  onMounted(() => {
    mm.add(
      '(prefers-reduced-motion: no-preference)',
      (context) => {
        motion = context
        return () => {
          motion = undefined
        }
      },
      scope.value
    )
  })
  onUnmounted(() => mm.revert())

  return (animate: () => void) => {
    if (motion && !isReducedMotion()) motion.add(animate)
  }
}
