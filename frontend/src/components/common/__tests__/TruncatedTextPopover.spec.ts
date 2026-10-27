import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, describe, expect, it } from 'vitest'

import TruncatedTextPopover from '../TruncatedTextPopover.vue'

const text = 'First line\nA description long enough to be clamped'
let wrapper: VueWrapper | undefined

const mountText = (enabled: boolean, truncated: boolean) => {
  wrapper = mount(TruncatedTextPopover, { props: { text, enabled }, attachTo: document.body })
  const el = wrapper.element as HTMLElement
  Object.defineProperty(el, 'clientHeight', { configurable: true, value: 59 })
  Object.defineProperty(el, 'scrollHeight', { configurable: true, value: truncated ? 98 : 59 })
  el.getBoundingClientRect = () => ({ x: 40, y: 100, left: 40, top: 100, right: 740, bottom: 159, width: 700, height: 59, toJSON: () => ({}) })
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
    expect(popover()?.textContent?.trim()).toBe(text)
    expect(popover()?.style.top).toBe('165px')
    expect(popover()?.style.width).toBe('700px')

    await view.trigger('mouseleave')
    expect(popover()).toBeNull()
  })

  it('stays hidden when the text fits', async () => {
    await mountText(true, false).trigger('mouseenter')
    expect(popover()).toBeNull()
  })

  it('stays hidden unless enabled', async () => {
    await mountText(false, true).trigger('mouseenter')
    expect(popover()).toBeNull()
  })

  it('closes when the page scrolls', async () => {
    const view = mountText(true, true)
    await view.trigger('mouseenter')
    window.dispatchEvent(new Event('scroll'))
    await view.vm.$nextTick()
    expect(popover()).toBeNull()
  })
})
