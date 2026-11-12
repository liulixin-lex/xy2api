import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h } from 'vue'

import AnnouncementPopup from '../AnnouncementPopup.vue'
import BaseDialog from '../BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { useStepUp } from '@/composables/useStepUp'
import { useAnnouncementStore } from '@/stores/announcements'

const { markRead, stepUp } = vi.hoisted(() => ({ markRead: vi.fn(), stepUp: vi.fn() }))
vi.mock('@/api', () => ({ announcementsAPI: { markRead, list: vi.fn() }, totpAPI: { stepUp } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

// The popup is mounted once for the whole app (App.vue) and shows whatever announcement arrives, also while a form
// dialog or the step-up dialog is open. It must then be the top modal layer: keyboard and focus are its own.
const announcement = {
  id: 7, title: 'Maintenance', content: 'Tonight', notify_mode: 'popup' as const,
  created_at: '2026-11-12T00:00:00Z', updated_at: '2026-11-12T00:00:00Z', read_at: null
}
const dialogClose = vi.fn()
let controller: ReturnType<typeof useStepUp>
const Host = defineComponent({
  setup() {
    controller = useStepUp()
    return () => [
      h(BaseDialog, { show: true, title: 'form', onClose: dialogClose }, {
        default: () => h('input', { 'data-test': 'name' }),
        footer: () => h('button', { 'data-test': 'save' }, 'save')
      }),
      h(TotpStepUpDialog, { controller }),
      h(AnnouncementPopup)
    ]
  }
})

enableAutoUnmount(afterEach)
beforeEach(() => {
  setActivePinia(createPinia())
  document.body.innerHTML = '<div id="app"></div>'
  document.body.className = ''
  document.body.style.overflow = ''
  document.documentElement.dataset.motion = 'reduced'
  markRead.mockReset().mockResolvedValue(undefined)
  dialogClose.mockReset()
})
afterAll(() => { delete document.documentElement.dataset.motion })

const q = (selector: string) => document.querySelector<HTMLElement>(selector)
const key = (name: string, shiftKey = false) => {
  const event = new KeyboardEvent('keydown', { key: name, shiftKey, bubbles: true, cancelable: true })
  document.activeElement!.dispatchEvent(event)
  return event
}

async function openForm() {
  mount(Host, { attachTo: '#app' })
  await flushPromises()
  const input = q('[data-test="name"]')!
  input.focus()
  return input
}

async function announce() {
  useAnnouncementStore().currentPopup = { ...announcement }
  await flushPromises()
}

describe('announcement popup arriving over open layers', () => {
  it('takes focus and the keyboard from an open form, and Escape marks it read without closing the form', async () => {
    const input = await openForm()
    await announce()
    const popup = q('[data-announcement-popup]')!
    expect(popup.closest('#app')).toBeNull()
    expect(popup.closest('[inert]')).toBeNull()
    expect(q('[data-base-dialog]')!.inert).toBe(true)
    expect(Number(popup.style.zIndex)).toBeGreaterThan(Number(q('[data-base-dialog]')!.style.zIndex))
    expect(popup.contains(document.activeElement)).toBe(true)

    // Tab and Shift+Tab stay in the popup.
    for (const shift of [false, true, false]) {
      const event = key('Tab', shift)
      expect(event.defaultPrevented).toBe(true)
      expect(popup.contains(document.activeElement)).toBe(true)
    }

    key('Escape')
    await flushPromises()
    expect(markRead).toHaveBeenCalledWith(7)
    // Gone from the store; its leave transition never ends in jsdom.
    expect(useAnnouncementStore().currentPopup).toBeNull()
    expect(dialogClose).not.toHaveBeenCalled()
    expect(q('[data-base-dialog]')!.inert).toBe(false)
    expect(document.activeElement).toBe(input)
  })

  it('gives focus back to the form after its own button', async () => {
    const input = await openForm()
    await announce()
    q('[data-testid="announcement-popup-dismiss"]')!.click()
    await flushPromises()
    expect(markRead).toHaveBeenCalledWith(7)
    // Gone from the store; its leave transition never ends in jsdom.
    expect(useAnnouncementStore().currentPopup).toBeNull()
    expect(document.activeElement).toBe(input)
  })

  it('stacks above the step-up dialog and returns focus to its code cell; the next Escape is the step-up\'s', async () => {
    await openForm()
    const save = q('[data-test="save"]')!
    save.focus()
    void controller.prompt()
    await flushPromises()
    const firstCell = q('[data-step-up-dialog] input[maxlength="1"]')!
    expect(document.activeElement).toBe(firstCell)

    await announce()
    const popup = q('[data-announcement-popup]')!
    expect(q('[data-step-up-dialog]')!.inert).toBe(true)
    expect(Number(popup.style.zIndex)).toBeGreaterThan(Number(q('[data-step-up-dialog]')!.style.zIndex))
    expect(popup.contains(document.activeElement)).toBe(true)

    key('Escape')
    await flushPromises()
    // Gone from the store; its leave transition never ends in jsdom.
    expect(useAnnouncementStore().currentPopup).toBeNull()
    expect(q('[data-step-up-dialog]')).not.toBeNull()
    expect(q('[data-step-up-dialog]')!.inert).toBe(false)
    expect(document.activeElement).toBe(firstCell)

    key('Escape')
    await flushPromises()
    expect(q('[data-step-up-dialog]')).toBeNull()
    expect(dialogClose).not.toHaveBeenCalled()
    expect(document.activeElement).toBe(save)
  })
})
