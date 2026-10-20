import { onMounted, type Ref } from 'vue'
import { isReducedMotion } from './useMotionPreference'

const MAX_SECTIONS = 6
const STAGGER_MS = 45
const DURATION_MS = 420
const EASING = 'cubic-bezier(0.22, 1, 0.36, 1)'
// 起始透明度不设为 0：内容从第一帧起就看得清，只是略淡并在下方 8px；
// 从 0 淡入时，Chromium 要等动画结束才记录 LCP，实测仪表盘晚了约 500ms。
const START_OPACITY = 0.4

/**
 * 页面区块入场：挂载时对容器下第二层的前 6 个区块做一次上移并由淡转实（错峰 45ms，总计不超过 270ms）。
 * 只处理挂载那一刻已存在的区块；之后因数据加载、轮询插入的内容不会再播放，表格行也不会动。
 * 用 Web Animations API 而不是切换 class：不触发整棵子树的样式重算，且 transform/opacity 动画走合成线程。
 */
export function usePageEntrance(containerRef: Ref<HTMLElement | null>) {
  onMounted(() => {
    const container = containerRef.value
    if (!container || isReducedMotion() || typeof Element === 'undefined' || !('animate' in Element.prototype)) return
    const sections: HTMLElement[] = []
    for (const root of Array.from(container.children)) {
      for (const child of Array.from(root.children)) {
        if (sections.length >= MAX_SECTIONS) break
        if (child instanceof HTMLElement && child.tagName !== 'DIALOG') sections.push(child)
      }
    }
    sections.forEach((section, index) => {
      section.animate(
        [
          { opacity: START_OPACITY, transform: 'translate3d(0, 8px, 0)' },
          { opacity: 1, transform: 'none' }
        ],
        { duration: DURATION_MS, delay: index * STAGGER_MS, easing: EASING, fill: 'backwards' }
      )
    })
  })
}
