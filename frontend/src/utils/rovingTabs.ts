import type { Directive } from 'vue'

/**
 * v-roving-tabs：挂在 role="tablist" 的容器上，给其中的 role="tab" 按钮补上键盘操作。
 * - ←/→（竖排时 ↑/↓ 也可）切换并选中相邻标签，Home/End 跳到首尾，循环移动；
 * - 只有选中的标签留在 Tab 顺序里（tabindex=0），其余为 -1；
 * - 选中通过触发按钮自身的 click 完成，组件原有的切换逻辑不需要改。
 */
const KEY_STEP: Record<string, number> = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }

type TablistElement = HTMLElement & { __rovingTabsKeydown?: (event: KeyboardEvent) => void }

const tabsOf = (container: HTMLElement) =>
  Array.from(container.querySelectorAll<HTMLElement>('[role="tab"]')).filter(
    (tab) => tab.closest('[role="tablist"]') === container && !tab.hasAttribute('disabled') && tab.getAttribute('aria-disabled') !== 'true'
  )

function syncTabindex(container: HTMLElement) {
  const tabs = tabsOf(container)
  const selected = tabs.find((tab) => tab.getAttribute('aria-selected') === 'true') ?? tabs[0]
  for (const tab of tabs) tab.tabIndex = tab === selected ? 0 : -1
}

export const vRovingTabs: Directive<TablistElement> = {
  mounted(el) {
    el.__rovingTabsKeydown = (event: KeyboardEvent) => {
      if (event.altKey || event.ctrlKey || event.metaKey) return
      const tabs = tabsOf(el)
      const current = tabs.indexOf(event.target as HTMLElement)
      if (current < 0) return
      let next: number
      if (event.key === 'Home') next = 0
      else if (event.key === 'End') next = tabs.length - 1
      else if (event.key in KEY_STEP) {
        const vertical = el.getAttribute('aria-orientation') === 'vertical'
        const horizontalKey = event.key === 'ArrowLeft' || event.key === 'ArrowRight'
        if (vertical === horizontalKey) return
        next = (current + KEY_STEP[event.key] + tabs.length) % tabs.length
      } else return
      event.preventDefault()
      const target = tabs[next]
      target.focus()
      target.click()
    }
    el.addEventListener('keydown', el.__rovingTabsKeydown)
    syncTabindex(el)
  },
  updated(el) {
    syncTabindex(el)
  },
  beforeUnmount(el) {
    if (el.__rovingTabsKeydown) el.removeEventListener('keydown', el.__rovingTabsKeydown)
    delete el.__rovingTabsKeydown
  }
}
