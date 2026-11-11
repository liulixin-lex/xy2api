import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, nextTick, ref } from 'vue'

import BaseDrawer from '../BaseDrawer.vue'
import BaseDialog from '../BaseDialog.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

enableAutoUnmount(afterEach)
afterEach(() => {
  delete document.documentElement.dataset.motion
  document.body.innerHTML = ''
})

const pressEscape = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
const panel = () => document.querySelector<HTMLElement>('[data-drawer-panel]')

// A trigger button that opens the drawer, the way a list row would.
function mountWithTrigger(props: Record<string, unknown> = {}) {
  const open = ref(false)
  const Host = defineComponent({
    setup: () => () => [
      h('button', { 'data-test': 'trigger', onClick: () => { open.value = true } }, 'open'),
      h(BaseDrawer, { show: open.value, title: 'Account 479', onClose: () => { open.value = false }, ...props }, {
        default: () => h('input', { 'data-test': 'field' })
      })
    ]
  })
  const wrapper = mount(Host, { attachTo: document.body, global: { stubs: { Icon: true } } })
  return { wrapper, open, trigger: () => wrapper.get<HTMLButtonElement>('[data-test="trigger"]').element }
}

describe('BaseDrawer', () => {
  it('opens in the body as a labelled modal dialog, closes on Escape and gives focus back to the trigger', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const { open, trigger } = mountWithTrigger()
    trigger().focus()
    trigger().click()
    await flushPromises()

    const dialog = panel()!
    expect(dialog.closest('[data-base-drawer]')!.parentElement).toBe(document.body)
    expect(dialog.getAttribute('role')).toBe('dialog')
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(document.getElementById(dialog.getAttribute('aria-labelledby')!)!.textContent).toBe('Account 479')
    expect(dialog.contains(document.activeElement)).toBe(true)
    expect(document.body.classList.contains('modal-open')).toBe(true)

    pressEscape()
    await flushPromises()
    expect(open.value).toBe(false)
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(trigger())
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })

  it('closes from the overlay unless told not to', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const closable = mount(BaseDrawer, { props: { show: true, title: 'A' }, attachTo: document.body, global: { stubs: { Icon: true } } })
    document.querySelector<HTMLElement>('[data-drawer-overlay]')!.click()
    expect(closable.emitted('close')).toHaveLength(1)
    closable.unmount()

    const pinned = mount(BaseDrawer, { props: { show: true, title: 'B', closeOnOverlay: false }, attachTo: document.body, global: { stubs: { Icon: true } } })
    document.querySelector<HTMLElement>('[data-drawer-overlay]')!.click()
    expect(pinned.emitted('close')).toBeUndefined()
  })

  it('leaves Escape and the scroll lock to a dialog opened above it', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const drawer = mount(BaseDrawer, { props: { show: true, title: 'Drawer' }, attachTo: document.body, global: { stubs: { Icon: true } } })
    const dialog = mount(BaseDialog, { props: { show: true, title: 'Confirm' }, global: { stubs: { Icon: true } } })

    pressEscape()
    expect(dialog.emitted('close')).toHaveLength(1)
    expect(drawer.emitted('close')).toBeUndefined()

    await dialog.setProps({ show: false })
    expect(document.body.classList.contains('modal-open')).toBe(true)
    pressEscape()
    expect(drawer.emitted('close')).toHaveLength(1)
  })

  it('shows without animation under reduced motion and animates transform and opacity otherwise', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const still = mountWithTrigger()
    still.trigger().click()
    await nextTick()
    expect(panel()!.style.transform).toBe('')
    expect(panel()!.style.opacity).toBe('')
    still.wrapper.unmount()

    document.documentElement.dataset.motion = 'full'
    const moving = mountWithTrigger()
    moving.trigger().click()
    await nextTick()
    expect(panel()!.style.transform).toContain('translate')
    expect(Number(panel()!.style.opacity)).toBeLessThan(1)

    // Unmounting mid-animation reverts the inline styles.
    const element = panel()!
    moving.wrapper.unmount()
    expect(element.style.transform).toBe('')
  })
})
