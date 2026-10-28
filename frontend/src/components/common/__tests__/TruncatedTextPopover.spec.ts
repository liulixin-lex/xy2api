import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import TruncatedTextPopover from '../TruncatedTextPopover.vue'

const text = 'First line\nA description long enough to be clamped'
let wrapper: VueWrapper | undefined

const mountText = (enabled: boolean, truncated: boolean, top = 100) => {
  const host = document.createElement('div')
  document.body.appendChild(host)
  wrapper = mount(TruncatedTextPopover, { props: { text, enabled }, attachTo: host })
  const el = wrapper.element as HTMLElement
  Object.defineProperty(el, 'clientHeight', { configurable: true, value: 59 })
  Object.defineProperty(el, 'scrollHeight', { configurable: true, value: truncated ? 98 : 59 })
  el.getBoundingClientRect = () => ({ x: 40, y: top, left: 40, top, right: 740, bottom: top + 59, width: 700, height: 59, toJSON: () => ({}) })
  return wrapper
}

const popover = () => document.body.querySelector<HTMLElement>('[data-test="truncated-text-popover"]')

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  document.body.innerHTML = ''
})

describe('TruncatedTextPopover', () => {
  it('shows the full text below a truncated line on hover and hides it on leave', async () => {
    const view = mountText(true, true)
    await view.trigger('mouseenter')
    const pop = popover()
    expect(pop?.textContent?.trim()).toBe(text)
    expect(pop?.parentElement).toBe(document.body)
    expect(pop?.classList.contains('z-[100000030]')).toBe(true)
    expect(pop?.style.top).toBe('165px')
    expect(pop?.style.maxHeight).toBe(`${window.innerHeight - 159 - 14}px`)
    expect(pop?.style.width).toBe('700px')

    await view.trigger('mouseleave')
    expect(popover()).toBeNull()
  })

  it('opens above when there is more room above', async () => {
    const top = window.innerHeight - 120
    await mountText(true, true, top).trigger('mouseenter')
    expect(popover()?.style.top).toBe('')
    expect(popover()?.style.bottom).toBe(`${window.innerHeight - top + 6}px`)
    expect(popover()?.style.maxHeight).toBe(`${top - 14}px`)
  })

  it('stays hidden when the text fits', async () => {
    await mountText(true, false).trigger('mouseenter')
    expect(popover()).toBeNull()
  })

  it('stays hidden unless enabled', async () => {
    await mountText(false, true).trigger('mouseenter')
    expect(popover()).toBeNull()
  })

  it('closes when any scroll container scrolls', async () => {
    const view = mountText(true, true)
    await view.trigger('mouseenter')
    // scroll 事件不冒泡，只有捕获阶段的监听能收到列表自身的滚动
    view.element.parentElement!.dispatchEvent(new Event('scroll'))
    await view.vm.$nextTick()
    expect(popover()).toBeNull()
  })
})
