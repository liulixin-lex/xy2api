import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import GroupCategoryTabs from '../GroupCategoryTabs.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('GroupCategoryTabs keyboard navigation', () => {
  it.each([1, 3])('moves focus without toggling with %i tabs, while clicks still toggle', async (count) => {
    const tabs = Array.from({ length: count }, (_, i) => ({ id: i + 1, name: `c${i}` }))
    const wrapper = mount(GroupCategoryTabs, { props: { tabs, activeId: 1 }, attachTo: document.body })
    try {
      const buttons = wrapper.findAll('button')
      const move = async (from: number, key: string, to: number) => {
        await buttons[from].trigger('keydown', { key })
        expect(document.activeElement).toBe(buttons[to].element)
        expect(buttons[to].attributes('tabindex')).toBe('0')
        expect(wrapper.emitted('toggle')).toBeUndefined()
      }
      await move(0, 'Home', 0)
      await move(0, 'ArrowLeft', count - 1)
      await move(count - 1, 'End', count - 1)
      await move(count - 1, 'ArrowRight', 0)
      await buttons[0].trigger('click')
      expect(wrapper.emitted('toggle')).toEqual([[1]])
      await wrapper.setProps({ activeId: null })
      await buttons[0].trigger('click')
      expect(wrapper.emitted('toggle')).toEqual([[1], [1]])
    } finally {
      wrapper.unmount()
    }
  })
})
