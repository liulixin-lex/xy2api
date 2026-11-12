import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import TotpSetupModal from '../TotpSetupModal.vue'
import { closeOverlay, openOverlay } from '@/utils/overlayStack'

const api = vi.hoisted(() => ({ getVerificationMethod: vi.fn(), initiateSetup: vi.fn(), enable: vi.fn() }))
vi.mock('@/api', () => ({ totpAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('qrcode', () => ({ default: { toDataURL: vi.fn().mockResolvedValue('data:image/png;base64,') } }))

// Identity check -> QR code (RT r3 R11): the next button is disabled during the request (the browser then drops
// its focus) and replaced by the QR step; focus must stay in the dialog unless another layer opened above.
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.resetAllMocks()
  document.body.innerHTML = '<div id="app"></div>'
  document.body.className = ''
  api.getVerificationMethod.mockResolvedValue({ method: 'password' })
})

let settle: { resolve: (value: unknown) => void; reject: (error: unknown) => void }

async function submitIdentity() {
  api.initiateSetup.mockImplementation(() => new Promise((resolve, reject) => { settle = { resolve, reject } }))
  mount(TotpSetupModal, { attachTo: '#app' })
  await flushPromises()
  const password = document.querySelector<HTMLInputElement>('[role="dialog"] input[type="password"]')!
  password.value = 'secret'
  password.dispatchEvent(new Event('input'))
  await flushPromises()
  const next = document.querySelector<HTMLButtonElement>('[data-setup-next]')!
  next.focus()
  next.click()
  await flushPromises()
  expect(next.disabled).toBe(true)
  // A browser moves focus off a button that becomes disabled; jsdom does not.
  next.blur()
  return next
}

describe('TOTP setup focus across the identity request', () => {
  it('moves focus to the QR step\'s next button when the request succeeds', async () => {
    await submitIdentity()
    settle.resolve({ secret: 'EXAMPLE', qr_code_url: 'otpauth://x', setup_token: 'token' })
    await flushPromises()
    const qrNext = document.querySelector<HTMLButtonElement>('[data-setup-next]')!
    expect(qrNext.disabled).toBe(false)
    expect(document.activeElement).toBe(qrNext)
    expect(qrNext.closest('[role="dialog"]')).not.toBeNull()
  })

  it('puts focus back on the next button when the request fails', async () => {
    const next = await submitIdentity()
    settle.reject({ message: 'wrong password' })
    await flushPromises()
    expect(next.isConnected && !next.disabled).toBe(true)
    expect(document.activeElement).toBe(next)
  })

  it('leaves focus alone when another layer opened above during the request', async () => {
    await submitIdentity()
    const upper = document.createElement('div')
    const button = document.createElement('button')
    upper.appendChild(button)
    document.body.appendChild(upper)
    const token = Symbol('upper')
    openOverlay(token, { root: () => upper, panel: () => upper })
    button.focus()
    settle.resolve({ secret: 'EXAMPLE', qr_code_url: 'otpauth://x', setup_token: 'token' })
    await flushPromises()
    expect(document.activeElement).toBe(button)
    closeOverlay(token)
  })
})
