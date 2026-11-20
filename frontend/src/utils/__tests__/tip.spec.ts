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

// A scroll hides any tip (spec E). Tabbing to an element below the fold scrolls it into view (smoothly: html has
// scroll-behavior: smooth), so a keyboard-focused element's tip waits until no scroll has come for two frames.
describe('v-tip on keyboard focus and scroll', () => {
  const frame = () => new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
  const scroll = () => document.dispatchEvent(new Event('scroll'))
  const shown = () => !(document.getElementById('sq-tip')?.hidden ?? true)
  const focusHost = () => {
    const el = mount(Host, { attachTo: document.body }).element as HTMLElement
    el.focus()
    return el
  }

  it('shows a keyboard-focused tip once the focus has scrolled its element into view; a later scroll hides it', async () => {
    content.value = 'tip text'
    const el = focusHost()
    // The focus scroll, one scroll event a frame: no tip yet.
    for (let i = 0; i < 3; i++) {
      scroll()
      await frame()
    }
    expect(shown()).toBe(false)
    await frame()
    expect([shown(), el.getAttribute('aria-describedby')]).toEqual([true, 'sq-tip'])
    // The user's own scroll hides it as any other.
    scroll()
    expect([shown(), el.getAttribute('aria-describedby')]).toEqual([false, null])
    // Nothing to scroll: two frames.
    el.blur()
    el.focus()
    await frame()
    expect(shown()).toBe(false)
    await frame()
    expect(shown()).toBe(true)
  })

  it('closes a hover tip on scroll, also one hovered open again on a keyboard-focused element after Escape', async () => {
    content.value = 'tip text'
    const hovered = mount(Host, { attachTo: document.body }).element as HTMLElement
    hover(hovered)
    vi.useRealTimers()
    expect(shown()).toBe(true)
    scroll()
    expect([shown(), hovered.getAttribute('aria-describedby')]).toEqual([false, null])

    const focused = focusHost()
    await frame()
    await frame()
    expect(shown()).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(shown()).toBe(false)
    hover(focused)
    vi.useRealTimers()
    expect([shown(), document.activeElement === focused]).toEqual([true, true])
    scroll()
    expect([shown(), focused.getAttribute('aria-describedby')]).toEqual([false, null])
  })

  it('closes a keyboard-focused tip on Escape and on blur, and shows none when the focus leaves before it settles', async () => {
    content.value = 'tip text'
    const el = focusHost()
    await frame()
    await frame()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect([shown(), el.getAttribute('aria-describedby')]).toEqual([false, null])
    el.blur()
    el.focus()
    await frame()
    await frame()
    expect(shown()).toBe(true)
    el.blur()
    expect([shown(), el.getAttribute('aria-describedby')]).toEqual([false, null])
    el.focus()
    el.blur()
    await frame()
    await frame()
    expect(shown()).toBe(false)
  })

  // A fixed box is as wide as its text but no wider than the room right of its left: measured at its last left, a tip
  // after one further right came out narrower and moved on the next showing (320px screen).
  it('measures where nothing squeezes it: the same place on a narrow screen whichever tip showed before', () => {
    vi.stubGlobal('innerWidth', 320)
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function (this: HTMLElement) {
      return Math.min((this.textContent?.length ?? 0) * 10, 320 - (Number.parseFloat(this.style.left) || 0))
    })
    const tipOn = (text: string, left: number) => {
      const el = mount(defineComponent({ setup: () => () => withDirectives(h('button', { type: 'button' }, 'x'), [[vTip, text]]) }), { attachTo: document.body }).element as HTMLElement
      vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top: 300, bottom: 320, left, width: 24, height: 20 } as DOMRect)
      return el
    }
    const right = tipOn('ok', 290)
    const wide = tipOn('x'.repeat(30), 150)
    const showOn = (el: HTMLElement) => {
      hover(el)
      const at = [tip().style.left, tip().style.top]
      el.dispatchEvent(new Event('mouseleave'))
      return at
    }
    expect(showOn(right)).toEqual(['292px', '294px'])
    expect(showOn(wide)).toEqual(['12px', '294px'])
    expect(showOn(wide)).toEqual(['12px', '294px'])
  })
})
