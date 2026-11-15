import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, ref, withDirectives } from 'vue'
import { enableAutoUnmount, mount } from '@vue/test-utils'

import { vTip, type TipContent } from '../tip'

const content = ref<TipContent>('')
const Host = defineComponent({ setup: () => () => withDirectives(h('button', { type: 'button' }, 'x'), [[vTip, content.value]]) })

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

function hover(el: Element) {
  vi.useFakeTimers()
  el.dispatchEvent(new Event('mouseenter'))
  vi.advanceTimersByTime(400)
}
const tip = () => document.getElementById('sq-tip')!
const texts = (selector: string) => Array.from(tip().querySelectorAll(selector), (el) => el.textContent)

// Tip cards (12.7): data in rows and columns, built from text nodes.
describe('v-tip cards', () => {
  it('draws the title, the state with its dot, the column heads and the rows, the first and the note column marked', () => {
    content.value = {
      title: '晨星组',
      chip: { text: '后排', tone: 'back', dot: true },
      head: ['模型', '原因', '恢复'],
      rows: [['Astra', { text: '首字超限', tone: 'back' }, '15m 后恢复'], ['<b>x</b>', 'y', '']]
    }
    hover(mount(Host, { attachTo: document.body }).element)
    expect(tip().classList.contains('sq-tip--card')).toBe(true)
    expect(texts('.sq-tip__title')).toEqual(['晨星组'])
    const chip = tip().querySelector('.sq-tip__chip')!
    expect([chip.textContent, chip.classList.contains('sq-tip__tone--back'), !!chip.querySelector('.sq-tip__dot')]).toEqual(['后排', true, true])
    expect(texts('.sq-tip__h')).toEqual(['模型', '原因', '恢复'])
    expect(texts('.sq-tip__c')).toEqual(['Astra', '首字超限', '15m 后恢复', '<b>x</b>', 'y', ''])
    expect(tip().querySelector('b')).toBeNull()
    expect(texts('.sq-tip__first')).toEqual(['模型', 'Astra', '<b>x</b>'])
    expect(texts('.sq-tip__end')).toEqual(['恢复', '15m 后恢复', ''])
    expect(tip().querySelectorAll('.sq-tip__c.sq-tip__tone--back')).toHaveLength(1)
  })

  it('has no note column with two columns, and no data area without rows', () => {
    content.value = { title: 'g', rows: [['豁免至', '长期']] }
    hover(mount(Host, { attachTo: document.body }).element)
    expect(texts('.sq-tip__first')).toEqual(['豁免至'])
    expect(tip().querySelector('.sq-tip__end')).toBeNull()

    content.value = { title: 'g', chip: { text: '无质量数据', tone: 'muted' } }
    hover(mount(Host, { attachTo: document.body }).element)
    expect(tip().querySelector('.sq-tip__grid')).toBeNull()
    expect(tip().querySelector('.sq-tip__dot')).toBeNull()
  })

  it('goes back to plain text when the next tip is a string, and redraws a shown tip whose value changes', async () => {
    content.value = { title: 'g', rows: [['a', 'b']] }
    const wrapper = mount(Host, { attachTo: document.body })
    hover(wrapper.element)
    content.value = 'line 1\nline 2'
    await wrapper.vm.$nextTick()
    expect(tip().className).toBe('sq-tip')
    expect(tip().children).toHaveLength(0)
    expect(tip().textContent).toBe('line 1\nline 2')
  })

  it('goes below when it does not fit above, and against the bottom edge when it fits neither', () => {
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(500)
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(200)
    content.value = 'tip'
    const el = mount(Host, { attachTo: document.body }).element as HTMLElement
    const at = (top: number) => {
      vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top, bottom: top + 20, left: 100, width: 40, height: 20 } as DOMRect)
      el.dispatchEvent(new Event('mouseleave'))
      hover(el)
      vi.useRealTimers()
      return tip().style.top
    }
    expect(window.innerHeight).toBe(768)
    expect(at(600)).toBe('94px')
    expect(at(100)).toBe('126px')
    expect(at(300)).toBe('260px')
  })
})
