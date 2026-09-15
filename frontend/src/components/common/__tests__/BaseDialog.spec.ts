import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, nextTick, ref } from 'vue'
import BaseDialog from '../BaseDialog.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('BaseDialog', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.body.classList.remove('modal-open')
  })

  it('restores focus to the opener when an open dialog is unmounted', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const wrapper = mount(BaseDialog, { attachTo: document.body, props: { show: true, title: 'IQ records' } })
    await nextTick()
    expect(document.activeElement).not.toBe(opener)
    wrapper.unmount()
    expect(document.activeElement).toBe(opener)
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })

  it('keeps Tab and Shift+Tab within the dialog', async () => {
    const wrapper = mount(BaseDialog, { attachTo: document.body, props: { show: true, title: 'IQ records' }, slots: { default: '<button id="last-action">Run</button>' } })
    await nextTick()
    const first = document.body.querySelector<HTMLButtonElement>('.modal-header button')!
    const last = document.getElementById('last-action')!
    last.focus()
    last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(first)
    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(last)
    wrapper.unmount()
  })

  it('keeps the parent modal locked and focused when a nested dialog closes', async () => {
    const showInner = ref(false)
    const wrapper = mount(defineComponent({
      components: { BaseDialog },
      setup: () => ({ showInner }),
      template: '<BaseDialog :show="true" title="Parent"><button id="nested-opener">Open</button></BaseDialog><BaseDialog v-if="showInner" :show="true" title="Child" @close="showInner = false" />'
    }), { attachTo: document.body })
    await nextTick()
    const opener = document.getElementById('nested-opener')!
    opener.focus()
    showInner.value = true
    await nextTick(); await nextTick()
    const titles = Array.from(document.querySelectorAll('[role="dialog"]')).map(el => el.getAttribute('aria-labelledby'))
    expect(new Set(titles).size).toBe(2)
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await nextTick()
    expect(document.querySelectorAll('[role="dialog"]')).toHaveLength(1)
    expect(document.body.classList.contains('modal-open')).toBe(true)
    expect(document.activeElement).toBe(opener)
    wrapper.unmount()
  })

  it('releases scrolling when two mounted dialogs close in the same update', async () => {
    const show = ref(true)
    const wrapper = mount(defineComponent({ components: { BaseDialog }, setup: () => ({ show }), template: '<BaseDialog :show="show" title="One" /><BaseDialog :show="show" title="Two" />' }), { attachTo: document.body })
    await nextTick()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    show.value = false
    await nextTick()
    expect(document.body.classList.contains('modal-open')).toBe(false)
    wrapper.unmount()
  })

  it('resets body scroll position when reopened', async () => {
    const wrapper = mount(BaseDialog, {
      attachTo: document.body,
      props: { show: false, title: 'Details' },
      slots: { default: '<div style="height: 2000px">content</div>' },
      global: { stubs: { Icon: true } }
    })

    await wrapper.setProps({ show: true })
    await nextTick()
    const body = document.body.querySelector<HTMLElement>('.modal-body')
    expect(body).not.toBeNull()
    body!.scrollTop = 480

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await nextTick()

    expect(document.body.querySelector<HTMLElement>('.modal-body')?.scrollTop).toBe(0)
    wrapper.unmount()
  })
})
