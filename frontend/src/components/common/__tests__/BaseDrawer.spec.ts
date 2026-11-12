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

// Two layers opened from each other: the one opened last is drawn on top (higher z-index, so it takes the
// clicks), holds focus and gets Escape; closing it hands focus back to the button that opened it.
describe('BaseDrawer and BaseDialog layer order', () => {
  const layerZ = (selector: string) => Number(document.querySelector<HTMLElement>(selector)!.style.zIndex)
  const inside = (selector: string) => !!document.querySelector(selector)?.contains(document.activeElement)
  const button = (name: string) => document.querySelector<HTMLButtonElement>(`[data-test="${name}"]`)!
  const press = async (name: string) => {
    button(name).focus()
    button(name).click()
    await flushPromises()
  }

  // The page opens the first layer; a button inside each layer opens the other one.
  function mountLayers(first: 'drawer' | 'dialog', dialogZ?: number) {
    const drawerOpen = ref(false)
    const dialogOpen = ref(false)
    const Host = defineComponent({
      setup: () => () => [
        h('button', { 'data-test': 'page', onClick: () => { (first === 'drawer' ? drawerOpen : dialogOpen).value = true } }, 'open'),
        h(BaseDrawer, { show: drawerOpen.value, title: 'Account', onClose: () => { drawerOpen.value = false } }, {
          default: () => h('button', { 'data-test': 'in-drawer', onClick: () => { dialogOpen.value = true } }, 'reset')
        }),
        h(BaseDialog, { show: dialogOpen.value, title: 'Confirm', zIndex: dialogZ, onClose: () => { dialogOpen.value = false } }, {
          default: () => h('button', { 'data-test': 'in-dialog', onClick: () => { drawerOpen.value = true } }, 'account')
        })
      ]
    })
    mount(Host, { attachTo: document.body, global: { stubs: { Icon: true } } })
    return { drawerOpen, dialogOpen }
  }

  it('puts a confirm dialog opened from the drawer on top', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const { drawerOpen, dialogOpen } = mountLayers('drawer')
    await press('page')
    await press('in-drawer')

    expect(layerZ('[data-base-dialog]')).toBeGreaterThan(layerZ('[data-base-drawer]'))
    expect(inside('[data-base-dialog]')).toBe(true)

    pressEscape()
    await flushPromises()
    expect([dialogOpen.value, drawerOpen.value]).toEqual([false, true])
    expect(document.activeElement).toBe(button('in-drawer'))

    pressEscape()
    await flushPromises()
    expect(drawerOpen.value).toBe(false)
    expect(document.activeElement).toBe(button('page'))
  })

  it('puts a drawer opened from a dialog on top, also above a dialog with a raised z-index', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const { drawerOpen, dialogOpen } = mountLayers('dialog', 60)
    await press('page')
    await press('in-dialog')

    expect(layerZ('[data-base-dialog]')).toBe(60)
    expect(layerZ('[data-base-drawer]')).toBeGreaterThan(60)
    expect(inside('[data-drawer-panel]')).toBe(true)

    pressEscape()
    await flushPromises()
    expect([drawerOpen.value, dialogOpen.value]).toEqual([false, true])
    expect(document.activeElement).toBe(button('in-dialog'))

    pressEscape()
    await flushPromises()
    expect(dialogOpen.value).toBe(false)
    expect(document.activeElement).toBe(button('page'))
  })

  it('leaves focus and the scroll lock with the upper layer when the lower one closes first', async () => {
    document.documentElement.dataset.motion = 'reduced'
    const { drawerOpen, dialogOpen } = mountLayers('drawer')
    await press('page')
    await press('in-drawer')

    drawerOpen.value = false
    await flushPromises()
    expect(document.querySelector('[data-base-drawer]')).toBeNull()
    expect(inside('[data-base-dialog]')).toBe(true)
    expect(document.body.classList.contains('modal-open')).toBe(true)

    dialogOpen.value = false
    await flushPromises()
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })
})
