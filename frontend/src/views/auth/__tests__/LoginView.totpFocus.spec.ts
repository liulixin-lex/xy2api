import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

const { getPublicSettingsMock, loginMock } = vi.hoisted(() => ({ getPublicSettingsMock: vi.fn(), loginMock: vi.fn() }))

vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn(), currentRoute: { value: { query: {} } } }) }))
vi.mock('vue-i18n', () => ({
  createI18n: () => ({ global: { t: (key: string) => key } }),
  useI18n: () => ({ t: (key: string) => key })
}))
vi.mock('@/stores', () => ({
  useAuthStore: () => ({ login: loginMock, loginWithPasskey: vi.fn(), login2FA: vi.fn() }),
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showWarning: vi.fn() })
}))
vi.mock('@/api/auth', () => ({
  buildOAuthLoginStartURL: vi.fn(),
  getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
  isTotp2FARequired: (response: { requires_2fa?: boolean }) => !!response?.requires_2fa,
  isWeChatWebOAuthEnabled: vi.fn(() => false),
  startOAuthLogin: vi.fn()
}))

const publicSettings = {
  registration_enabled: true, turnstile_enabled: false, turnstile_site_key: '', tencent_captcha_enabled: false,
  tencent_captcha_app_id: '', aliyun_captcha_enabled: false, aliyun_captcha_scene_id: '', aliyun_captcha_prefix: '',
  linuxdo_oauth_enabled: false, dingtalk_oauth_enabled: false, wechat_oauth_enabled: false, backend_mode_enabled: false,
  oidc_oauth_enabled: false, oidc_oauth_provider_name: 'OIDC', github_oauth_enabled: false, google_oauth_enabled: false,
  password_reset_enabled: false, passkey_enabled: false, login_agreement_enabled: false, login_agreement_documents: []
}

// Login that answers with a step-up challenge (RT r3 R12). The login button is disabled while the request runs,
// so the TOTP dialog opens with no focused opener; cancelling it must still bring focus back to the login button.
enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = '<div id="app"></div>'
  document.body.className = ''
  getPublicSettingsMock.mockReset().mockResolvedValue(publicSettings)
  loginMock.mockReset().mockImplementation(() => new Promise((resolve) => setTimeout(() => resolve({ requires_2fa: true, temp_token: 't', user_email_masked: 'a***@x.test' }), 10)))
})

async function fillForm() {
  mount(LoginView, {
    attachTo: '#app',
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        DingTalkOAuthSection: true, EmailOAuthButtons: true, Icon: true, LinuxDoOAuthSection: true,
        LoginAgreementPrompt: true, OidcOAuthSection: true, RouterLink: { template: '<a><slot /></a>' },
        TurnstileWidget: true, WechatOAuthSection: true, transition: false
      }
    }
  })
  await flushPromises()
  const email = document.querySelector<HTMLInputElement>('input[type="email"]')!
  const password = document.querySelector<HTMLInputElement>('input[type="password"]')!
  email.value = 'admin@example.test'
  email.dispatchEvent(new Event('input'))
  password.value = 'secret-password'
  password.dispatchEvent(new Event('input'))
  await flushPromises()
  return { password, button: document.querySelector<HTMLButtonElement>('button[type="submit"]')!, form: document.querySelector('form')! }
}

async function challenge(button: HTMLButtonElement) {
  await flushPromises()
  expect(button.disabled).toBe(true)
  // A browser moves focus off the login button while it is disabled; jsdom does not.
  ;(document.activeElement as HTMLElement | null)?.blur()
  await new Promise((resolve) => setTimeout(resolve, 20))
  await flushPromises()
  const firstCell = document.querySelector<HTMLInputElement>('[role="dialog"] input[maxlength="1"]')!
  expect(firstCell.closest('#app')).toBeNull()
  expect(document.activeElement).toBe(firstCell)
}

const escape = () => document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))

describe('login step-up cancelled', () => {
  it('returns focus to the login button after a mouse submit and Escape', async () => {
    const { button } = await fillForm()
    button.focus()
    button.click()
    await challenge(button)

    escape()
    await flushPromises()
    expect(document.querySelector('[role="dialog"] input[maxlength="1"]')).toBeNull()
    expect(button.disabled).toBe(false)
    expect(document.activeElement).toBe(button)
  })

  it('returns focus to the login button after an Enter submit and the cancel button', async () => {
    const { password, button, form } = await fillForm()
    password.focus()
    // Enter in the password field submits the form.
    form.dispatchEvent(new Event('submit', { cancelable: true }))
    await challenge(button)

    const cancel = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')).find((el) => el.textContent?.trim() === 'common.cancel')!
    cancel.click()
    await flushPromises()
    expect(document.querySelector('[role="dialog"] input[maxlength="1"]')).toBeNull()
    expect(document.activeElement).toBe(button)
  })
})
