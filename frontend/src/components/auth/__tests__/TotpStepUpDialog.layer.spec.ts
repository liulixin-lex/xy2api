import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'

import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '../TotpStepUpDialog.vue'
import { isStepUpCancelled, useStepUp } from '@/composables/useStepUp'

const { stepUp, showError } = vi.hoisted(() => ({ stepUp: vi.fn(), showError: vi.fn() }))
vi.mock('@/api', () => ({ totpAPI: { stepUp } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

// A form dialog whose save needs a step-up, with the step-up dialog next to it (as in UserCreateModal and the
// OpenAI TOTP dialogs): the step-up opens above the dialog and must be usable although the dialog made #app inert.
const outcome = ref<'saved' | 'cancelled' | null>(null)
const save = vi.fn()
const dialogClose = vi.fn()
const Host = defineComponent({
  setup() {
    const controller = useStepUp()
    const submit = async () => {
      try {
        await controller.run(save)
        outcome.value = 'saved'
      } catch (error) {
        if (isStepUpCancelled(error)) outcome.value = 'cancelled'
      }
    }
    return () => [
      h(BaseDialog, { show: true, title: 'form', onClose: dialogClose }, {
        default: () => h('input', { 'data-test': 'name' }),
        footer: () => h('button', { 'data-test': 'save', onClick: submit }, 'save')
      }),
      h(TotpStepUpDialog, { controller })
    ]
  }
})

enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = '<div id="app"></div>'
  document.body.className = ''
  document.documentElement.dataset.motion = 'reduced'
  outcome.value = null
  stepUp.mockReset().mockResolvedValue(undefined)
  save.mockReset().mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' }).mockResolvedValue(undefined)
  dialogClose.mockReset()
})
afterAll(() => { delete document.documentElement.dataset.motion })

const q = <T extends HTMLElement = HTMLElement>(selector: string) => document.querySelector<T>(selector)

async function openStepUp() {
  mount(Host, { attachTo: '#app' })
  await flushPromises()
  const saveButton = q('[data-test="save"]')!
  saveButton.focus()
  saveButton.click()
  await flushPromises()
  return saveButton
}

function codeInputs() {
  return Array.from(q('[data-step-up-dialog]')!.querySelectorAll<HTMLInputElement>('input[maxlength="1"]'))
}

describe('step-up dialog above a BaseDialog', () => {
  it('is the top modal layer: outside #app, not inert, the dialog below inert, focus on the first code cell', async () => {
    await openStepUp()
    const layer = q('[data-step-up-dialog]')!
    expect(layer.closest('#app')).toBeNull()
    expect(layer.closest('[inert]')).toBeNull()
    expect(q('#app')!.inert).toBe(true)
    expect(q('[data-base-dialog]')!.inert).toBe(true)
    expect(Number(layer.style.zIndex)).toBeGreaterThan(Number(q<HTMLElement>('[data-base-dialog]')!.style.zIndex))
    expect(document.activeElement).toBe(codeInputs()[0])

    // Tab stays inside the step-up dialog.
    const cancel = layer.querySelector<HTMLButtonElement>('button')!
    cancel.focus()
    const tab = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    cancel.dispatchEvent(tab)
    expect(tab.defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(codeInputs()[0])
  })

  it('verifies, retries the save and gives focus back to the dialog below', async () => {
    const saveButton = await openStepUp()
    codeInputs().forEach((input, index) => {
      input.value = String(index + 1)
      input.dispatchEvent(new Event('input'))
    })
    await flushPromises()
    expect(stepUp).toHaveBeenCalledWith('123456')
    expect(save).toHaveBeenCalledTimes(2)
    expect(outcome.value).toBe('saved')
    expect(q('[data-step-up-dialog]')).toBeNull()
    expect(q('[data-base-dialog]')!.inert).toBe(false)
    expect(q('#app')!.inert).toBe(true)
    expect(document.activeElement).toBe(saveButton)
  })

  it('cancels from its button and gives focus back to the dialog below, which stays open', async () => {
    const saveButton = await openStepUp()
    q('[data-step-up-dialog]')!.querySelector<HTMLButtonElement>('button')!.click()
    await flushPromises()
    expect(outcome.value).toBe('cancelled')
    expect(save).toHaveBeenCalledTimes(1)
    expect(q('[data-step-up-dialog]')).toBeNull()
    expect(q('[data-base-dialog]')).not.toBeNull()
    expect(document.activeElement).toBe(saveButton)
  })

  it('cancels on Escape without closing the dialog below; the next Escape is for the dialog', async () => {
    const saveButton = await openStepUp()
    const dialog = q('[data-base-dialog]')!
    const escape = () => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    escape()
    await flushPromises()
    expect(outcome.value).toBe('cancelled')
    expect(q('[data-step-up-dialog]')).toBeNull()
    expect(dialogClose).not.toHaveBeenCalled()
    expect(dialog.inert).toBe(false)
    expect(document.activeElement).toBe(saveButton)

    escape()
    expect(dialogClose).toHaveBeenCalledTimes(1)
  })
})
