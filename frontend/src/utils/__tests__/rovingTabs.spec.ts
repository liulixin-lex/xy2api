import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import { vRovingTabs } from '../rovingTabs'

const Tabs = defineComponent({
  directives: { RovingTabs: vRovingTabs },
  setup() {
    const active = ref('a')
    return { active, items: ['a', 'b', 'c'] }
  },
  template: `
    <div v-roving-tabs role="tablist">
      <button v-for="item in items" :key="item" type="button" role="tab" :data-tab="item"
        :aria-selected="active === item" @click="active = item">{{ item }}</button>
    </div>`
})

describe('vRovingTabs', () => {
  it('keeps only the selected tab in the tab order and moves with arrow/Home/End', async () => {
    const wrapper = mount(Tabs, { attachTo: document.body })
    const tab = (name: string) => wrapper.get(`[data-tab="${name}"]`).element as HTMLButtonElement
    expect([tab('a').tabIndex, tab('b').tabIndex, tab('c').tabIndex]).toEqual([0, -1, -1])

    await wrapper.get('[data-tab="a"]').trigger('keydown', { key: 'ArrowRight' })
    expect(wrapper.vm.active).toBe('b')
    expect(document.activeElement).toBe(tab('b'))
    expect([tab('a').tabIndex, tab('b').tabIndex]).toEqual([-1, 0])

    await wrapper.get('[data-tab="a"]').trigger('keydown', { key: 'ArrowLeft' })
    expect(wrapper.vm.active).toBe('c')

    await wrapper.get('[data-tab="c"]').trigger('keydown', { key: 'Home' })
    expect(wrapper.vm.active).toBe('a')
    await wrapper.get('[data-tab="a"]').trigger('keydown', { key: 'End' })
    expect(wrapper.vm.active).toBe('c')

    // 横排标签不响应 ↑/↓，留给页面滚动
    await wrapper.get('[data-tab="c"]').trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper.vm.active).toBe('c')
    wrapper.unmount()
  })
})
