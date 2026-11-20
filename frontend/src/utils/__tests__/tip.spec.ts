import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

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
  vi.unstubAllGlobals()
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

  // The recent status's card (wrapEnd) wraps its notes on a narrow screen; every other card keeps the note whole and
  // lets the label and value give way, as before.
  it('keeps the note column whole by default, and wraps it in what is left only when the card asks (wrapEnd)', () => {
    const grid = (card: TipContent) => {
      content.value = card
      hover(mount(Host, { attachTo: document.body }).element)
      const el = tip().querySelector<HTMLElement>('.sq-tip__grid')!
      // jsdom writes 0 as 0px.
      return [el.className, el.style.gridTemplateColumns.replaceAll('0px', '0')]
    }
    const rows = [['首字中位', '7.0s', '8 次，含超时 2 次 · 样本不足']]
    expect(grid({ title: 'g', rows })).toEqual(['sq-tip__grid', 'repeat(2, minmax(0, max-content)) minmax(max-content, 1fr)'])
    expect(grid({ title: 'g', rows, wrapEnd: true })).toEqual(['sq-tip__grid sq-tip__grid--wrap', 'repeat(2, max-content) minmax(0, 1fr)'])
    // Two columns have no note to wrap.
    expect(grid({ title: 'g', rows: [['豁免至', '长期']], wrapEnd: true })).toEqual(['sq-tip__grid', 'repeat(2, minmax(0, max-content))'])
    // A variable, not a literal: Vite rewrites new URL('<literal>', import.meta.url) into an asset URL.
    const file = '../../styles/schedQuality.css'
    const css = readFileSync(fileURLToPath(new URL(file, import.meta.url)), 'utf8')
    expect(css).toMatch(/\.sq-tip__grid--wrap \.sq-tip__end \{ white-space: normal; overflow-wrap: anywhere; \}/)
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

  it('takes a 320px viewport less 8px each side as its largest size before it is measured, then stays on screen (12.12-2)', () => {
    vi.stubGlobal('innerWidth', 320)
    vi.stubGlobal('innerHeight', 568)
    // The room the card has when it is measured; the browser sizes it to that room at most (304px wide here).
    const room: string[] = []
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function (this: HTMLElement) {
      room.push(this.style.getPropertyValue('--tip-room-w'), this.style.getPropertyValue('--tip-room-h'))
      return 304
    })
    content.value = { title: '晨星组', head: ['模型', '原因', '恢复'], rows: [['Astra', '首字超限', '15m 后恢复']] }
    const el = mount(Host, { attachTo: document.body }).element as HTMLElement
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top: 300, bottom: 320, left: 290, width: 24, height: 20 } as DOMRect)
    hover(el)
    expect(room).toEqual(['304px', '552px'])
    expect(tip().style.left).toBe('8px')
  })
})

// A scroll hides a tip (spec E), but for one whose target has keyboard focus: tabbing to an element below the fold
// scrolls it into view after its tip shows, and that tip must stay, moved with its target.
describe('v-tip on scroll', () => {
  const at = (el: Element, top: number) =>
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top, bottom: top + 20, left: 100, width: 24, height: 20 } as DOMRect)
  const scroll = () => document.dispatchEvent(new Event('scroll'))

  it('keeps a keyboard-focused tip through a scroll, moved with its target and still describing it', () => {
    content.value = 'tip text'
    const el = mount(Host, { attachTo: document.body }).element as HTMLElement
    at(el, 600)
    el.focus()
    expect([tip().hidden, tip().style.top, el.getAttribute('aria-describedby')]).toEqual([false, '594px', 'sq-tip'])
    at(el, 200)
    scroll()
    expect([tip().hidden, tip().style.top, el.getAttribute('aria-describedby')]).toEqual([false, '194px', 'sq-tip'])
  })

  it('still closes a hover tip on scroll, and a keyboard-focused one on Escape or blur', () => {
    content.value = 'tip text'
    const hovered = mount(Host, { attachTo: document.body }).element as HTMLElement
    hover(hovered)
    expect(tip().hidden).toBe(false)
    scroll()
    expect([tip().hidden, hovered.getAttribute('aria-describedby')]).toEqual([true, null])
    vi.useRealTimers()

    const focused = mount(Host, { attachTo: document.body }).element as HTMLElement
    focused.focus()
    expect(tip().hidden).toBe(false)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect([tip().hidden, focused.getAttribute('aria-describedby')]).toEqual([true, null])
    focused.blur()
    focused.focus()
    expect(tip().hidden).toBe(false)
    focused.blur()
    expect([tip().hidden, focused.getAttribute('aria-describedby')]).toEqual([true, null])
    // Once focus has gone, a scroll hides as before (nothing left to follow).
    scroll()
    expect(tip().hidden).toBe(true)
  })
})
