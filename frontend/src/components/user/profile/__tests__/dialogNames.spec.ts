import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import TotpSetupModal from '../TotpSetupModal.vue'
import TotpDisableDialog from '../TotpDisableDialog.vue'
import ProfilePasskeyCard from '../ProfilePasskeyCard.vue'
import TotpLoginModal from '@/components/auth/TotpLoginModal.vue'

const mocks = vi.hoisted(() => ({
  totpAPI: { getVerificationMethod: vi.fn(), sendVerifyCode: vi.fn(), initiateSetup: vi.fn(), enable: vi.fn(), disable: vi.fn() },
  passkeyAPI: { isSupported: vi.fn(() => true), list: vi.fn(), register: vi.fn(), rename: vi.fn(), remove: vi.fn() }
}))
vi.mock('@/api', () => ({ totpAPI: mocks.totpAPI, passkeyAPI: mocks.passkeyAPI }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('qrcode', () => ({ default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,qr') } }))

// RT r3 R13: each hand-built dialog is named by its visible title (aria-labelledby -> a unique id), without any
// added visible text.
enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = ''
  mocks.totpAPI.getVerificationMethod.mockResolvedValue({ method: 'password' })
  mocks.passkeyAPI.list.mockResolvedValue([])
})

function names() {
  return Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"][aria-modal="true"]')).map((dialog) => {
    const id = dialog.getAttribute('aria-labelledby')
    const title = id ? document.getElementById(id) : null
    return { id, inside: !!title && dialog.contains(title), text: title?.textContent?.trim() ?? '' }
  })
}

describe('hand-built dialogs are named by their visible title', () => {
  it.each([
    ['TOTP login', TotpLoginModal, { tempToken: 't' }, 'profile.totp.loginTitle'],
    ['TOTP setup', TotpSetupModal, {}, 'profile.totp.setupTitle'],
    ['TOTP disable', TotpDisableDialog, {}, 'profile.totp.disableTitle']
  ])('%s', async (_name, component, props, title) => {
    // Two at once in one app: their ids differ.
    mount(defineComponent({ setup: () => () => [h(component as never, props), h(component as never, props)] }), { attachTo: document.body })
    await flushPromises()
    const found = names()
    expect(found).toHaveLength(2)
    expect(found.every((entry) => entry.inside && entry.text === title)).toBe(true)
    expect(new Set(found.map((entry) => entry.id)).size).toBe(2)
  })

  it('passkey delete confirmation', async () => {
    const wrapper = mount(ProfilePasskeyCard, { props: { enabled: true }, attachTo: document.body })
    await flushPromises()
    ;(wrapper.vm as unknown as { deleteTarget: unknown }).deleteTarget = { id: 1, name: 'fixture-key', created_at: '', last_used_at: null }
    await flushPromises()
    expect(names()).toEqual([{ id: expect.stringMatching(/^passkey-delete-title-/), inside: true, text: 'profile.passkey.deleteTitle' }])
  })
})
