import { afterEach, describe, expect, it } from 'vitest'
import { nextTick } from 'vue'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import BaseDialog from '../BaseDialog.vue'

enableAutoUnmount(afterEach)

function dialog(show = true) {
  return mount(BaseDialog, { props: { show, title: 'Details' }, global: { stubs: { Icon: true } } })
}

const locked = () => document.body.classList.contains('modal-open')

describe('dialog body scroll lock', () => {
  it('keeps the body locked when a hidden sibling mounts or unmounts', () => {
    dialog()
    const hidden = dialog(false)
    expect(locked()).toBe(true)
    hidden.unmount()
    expect(locked()).toBe(true)
  })

  it('keeps the lock until the last visible dialog closes', async () => {
    const first = dialog()
    const second = dialog()
    await second.setProps({ show: false })
    expect(locked()).toBe(true)
    await first.setProps({ show: false })
    expect(locked()).toBe(false)
  })

  it('keeps the lock until the last visible dialog unmounts', () => {
    const first = dialog()
    const second = dialog()
    second.unmount()
    expect(locked()).toBe(true)
    first.unmount()
    expect(locked()).toBe(false)
  })

  it('releases the lock after repeated open and close cycles', async () => {
    const wrapper = dialog(false)
    expect(locked()).toBe(false)
    await wrapper.setProps({ show: true })
    expect(locked()).toBe(true)
    await wrapper.setProps({ show: false })
    expect(locked()).toBe(false)
    await wrapper.setProps({ show: true })
    wrapper.unmount()
    expect(locked()).toBe(false)
  })

  it('keeps focus and the lock in the upper dialog when the lower one closes first', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const lower = mount(BaseDialog, { props: { show: true, title: 'Lower' }, attachTo: document.body, global: { stubs: { Icon: true } } })
    const upper = mount(BaseDialog, { props: { show: true, title: 'Upper' }, attachTo: document.body, global: { stubs: { Icon: true } }, slots: { default: '<input data-test="upper-field" />' } })
    await nextTick()
    await nextTick()
    const field = document.querySelector<HTMLElement>('[data-test="upper-field"]')!
    field.focus()

    await lower.setProps({ show: false })
    expect(document.activeElement).toBe(field)
    expect(locked()).toBe(true)

    await upper.setProps({ show: false })
    expect(locked()).toBe(false)
    opener.remove()
  })
})
