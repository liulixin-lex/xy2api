import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '@/views/auth/RegisterView.vue'
import EmailVerifyView from '@/views/auth/EmailVerifyView.vue'
import { createMemoryHistory, createRouter, RouterView, type Router } from 'vue-router'
import { clearRegistrationPassword, takeRegistrationPassword } from '../registrationState'

enableAutoUnmount(afterEach)

const {
  getPublicSettingsMock,
  registerMock,
  sendVerifyCodeMock,
  showErrorMock,
  pushMock,
  verifyActionMock,
  appStoreMock,
  routerState
} = vi.hoisted(() => ({
  getPublicSettingsMock: vi.fn(),
  registerMock: vi.fn(),
  sendVerifyCodeMock: vi.fn(),
  showErrorMock: vi.fn(),
  pushMock: vi.fn(),
  verifyActionMock: vi.fn(),
  routerState: { router: null as Router | null },
  appStoreMock: {
    cachedPublicSettings: null as { promo_code_enabled?: boolean } | null,
    showError: (...args: unknown[]) => showErrorMock(...args),
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  }
}))

const publicSettings = {
  registration_enabled: true,
  email_verify_enabled: false,
  promo_code_enabled: false,
  invitation_code_enabled: false,
  affiliate_enabled: true,
  turnstile_enabled: true,
  turnstile_site_key: 'site-key',
  site_name: 'XY2API',
  registration_email_suffix_whitelist: [],
  linuxdo_oauth_enabled: false,
  wechat_oauth_enabled: false,
  oidc_oauth_enabled: false,
  github_oauth_enabled: false,
  google_oauth_enabled: false
}

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    onBeforeRouteLeave: (guard: Parameters<typeof actual.onBeforeRouteLeave>[0]) => {
      if (routerState.router) actual.onBeforeRouteLeave(guard)
    },
    useRouter: () => routerState.router || { push: pushMock },
    useRoute: () => routerState.router ? actual.useRoute() : { query: {} }
  }
})

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key
    }
  }),
  useI18n: () => ({
    t: (key: string) =>
      key === 'auth.emailDomainRegistrationLimit'
        ? '该邮箱域名无法注册新账户。请使用主流邮箱注册；如需使用企业邮箱，请联系客服添加域名白名单。'
        : key,
    locale: { value: 'en' }
  })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ register: (...args: unknown[]) => registerMock(...args) }),
  useAppStore: () => appStoreMock
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
    sendVerifyCode: (...args: unknown[]) => sendVerifyCodeMock(...args)
  }
})

function mountRegister(router?: Router) {
  return mount(router ? RouterView : RegisterView, {
    global: {
      plugins: router ? [router] : [],
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: true,
        TurnstileWidget: {
          template: '<div data-testid="turnstile-widget" />',
          methods: { verifyAction: verifyActionMock, reset: vi.fn() }
        },
        LoginAgreementPrompt: true,
        EmailOAuthButtons: true,
        LinuxDoOAuthSection: true,
        WechatOAuthSection: true,
        OidcOAuthSection: true,
        RouterLink: true,
        transition: false
      }
    }
  })
}

describe('RegisterView', () => {
  beforeEach(() => {
    getPublicSettingsMock.mockReset()
    registerMock.mockReset()
    sendVerifyCodeMock.mockReset()
    showErrorMock.mockReset()
    pushMock.mockReset()
    verifyActionMock.mockReset()
    appStoreMock.cachedPublicSettings = null
    routerState.router = null
    clearRegistrationPassword()
    sessionStorage.clear()
    localStorage.clear()
    verifyActionMock.mockResolvedValue({ token: 'ticket', randstr: 'randstr' })
    getPublicSettingsMock.mockResolvedValue(publicSettings)
    registerMock.mockResolvedValue({})
    sendVerifyCodeMock.mockResolvedValue({ countdown: 60 })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
    for (const storage of [sessionStorage, localStorage]) {
      for (let i = 0; i < storage.length; i++) {
        expect(storage.getItem(storage.key(i)!)).not.toContain('secret-123')
      }
    }
  })

  async function startVerificationFlow() {
    getPublicSettingsMock.mockResolvedValue({
      ...publicSettings, turnstile_enabled: false, email_verify_enabled: true
    })
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/register', component: RegisterView },
        { path: '/email-verify', component: EmailVerifyView },
        { path: '/dashboard', component: { template: '<div>Dashboard</div>' } }
      ]
    })
    routerState.router = router
    await router.push('/register')
    const wrapper = mountRegister(router)
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('#affiliate_code').setValue('AFF123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/email-verify')
    return { wrapper, router }
  }

  it('does not flash the promo-code field before disabled settings finish loading', async () => {
    let resolveSettings!: (settings: typeof publicSettings) => void
    getPublicSettingsMock.mockReturnValueOnce(
      new Promise<typeof publicSettings>((resolve) => {
        resolveSettings = resolve
      })
    )

    const wrapper = mountRegister()

    expect(wrapper.find('#promo_code').exists()).toBe(false)

    resolveSettings(publicSettings)
    await flushPromises()

    expect(wrapper.find('#promo_code').exists()).toBe(false)
  })

  it('uses injected public settings to show an enabled promo-code field on first render', () => {
    appStoreMock.cachedPublicSettings = { promo_code_enabled: true }
    getPublicSettingsMock.mockReturnValueOnce(new Promise(() => {}))

    const wrapper = mountRegister()

    expect(wrapper.find('#promo_code').exists()).toBe(true)
  })

  it.each([
    ['', 'auth.confirmPasswordRequired'],
    ['different-password', 'auth.passwordsDoNotMatch']
  ])('blocks invalid confirmation %j before captcha and allows correction', async (confirmation, error) => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'app-id'
    })
    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue(confirmation)
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith(error)
    expect(wrapper.get('#confirmPassword').classes()).toContain('input-error')
    expect(registerMock).not.toHaveBeenCalled()
    expect(verifyActionMock).not.toHaveBeenCalled()
    expect(pushMock).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('register_data')).toBeNull()

    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(wrapper.get('#confirmPassword').classes()).not.toContain('input-error')
    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(registerMock).toHaveBeenCalledWith({
      email: 'user@example.com',
      password: 'secret-123',
      turnstile_token: undefined,
      tencent_captcha_ticket: 'ticket',
      tencent_captcha_randstr: 'randstr',
      promo_code: undefined,
      invitation_code: undefined
    })
    expect(pushMock).toHaveBeenCalledWith('/dashboard')
    expect((wrapper.get('#password').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('#confirmPassword').element as HTMLInputElement).value).toBe('')
  })

  it('requires matching confirmation before storing only the registration fields for email verification', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      email_verify_enabled: true
    })
    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('different-password')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(sessionStorage.getItem('register_data')).toBeNull()
    expect(pushMock).not.toHaveBeenCalled()

    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(JSON.parse(sessionStorage.getItem('register_data')!)).toEqual({
      email: 'user@example.com'
    })
    expect(pushMock).toHaveBeenCalledWith('/email-verify')
    expect(registerMock).not.toHaveBeenCalled()
  })

  it('hands the password to verification in memory and never writes it to browser storage', async () => {
    const writes: string[] = []
    for (const name of ['sessionStorage', 'localStorage'] as const) {
      const storage = globalThis[name]
      vi.stubGlobal(name, new Proxy(storage, {
        get(target, key) {
          if (key === 'setItem') return (item: string, value: string) => {
            writes.push(value)
            target.setItem(item, value)
          }
          const value = Reflect.get(target, key, target)
          return typeof value === 'function' ? value.bind(target) : value
        }
      }))
    }
    const { wrapper, router } = await startVerificationFlow()
    expect(JSON.parse(sessionStorage.getItem('register_data')!)).toEqual({
      email: 'user@example.com', aff_code: 'AFF123'
    })
    await wrapper.get('#code').setValue('123456')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(expect.objectContaining({
      email: 'user@example.com', password: 'secret-123', aff_code: 'AFF123', verify_code: '123456'
    }))
    expect(sessionStorage.getItem('register_data')).toBeNull()
    expect(router.currentRoute.value.path).toBe('/dashboard')
    expect(writes.length).toBeGreaterThan(0)
    expect(writes.every((value) => !value.includes('secret-123'))).toBe(true)
  })

  it('returns from a refreshed verification page with a populated draft and empty password fields', async () => {
    const { wrapper, router } = await startVerificationFlow()
    wrapper.unmount()
    const refreshed = mountRegister(router)
    await flushPromises()

    expect(router.currentRoute.value.path).toBe('/register')
    expect((refreshed.get('#email').element as HTMLInputElement).value).toBe('user@example.com')
    expect((refreshed.get('#affiliate_code').element as HTMLInputElement).value).toBe('AFF123')
    expect((refreshed.get('#password').element as HTMLInputElement).value).toBe('')
    expect((refreshed.get('#confirmPassword').element as HTMLInputElement).value).toBe('')
    expect(sendVerifyCodeMock).toHaveBeenCalledOnce()
    expect(registerMock).not.toHaveBeenCalled()
  })

  it('clears the password on browser back and requires a new one before verification can resume', async () => {
    const { wrapper, router } = await startVerificationFlow()
    router.back()
    await flushPromises()

    expect(router.currentRoute.value.path).toBe('/register')
    expect((wrapper.get('#password').element as HTMLInputElement).value).toBe('')
    await router.push('/email-verify')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/register')
    expect(sendVerifyCodeMock).toHaveBeenCalledOnce()
    expect(registerMock).not.toHaveBeenCalled()
  })

  it('restores non-sensitive registration fields after verification is refreshed', async () => {
    sessionStorage.setItem('register_data', JSON.stringify({
      email: 'user@example.com', password: 'secret-123', promo_code: 'PROMO', invitation_code: 'INVITE', aff_code: 'AFF123'
    }))
    getPublicSettingsMock.mockResolvedValueOnce({ ...publicSettings, promo_code_enabled: true })
    const wrapper = mountRegister()
    await flushPromises()

    expect((wrapper.get('#email').element as HTMLInputElement).value).toBe('user@example.com')
    expect((wrapper.get('#promo_code').element as HTMLInputElement).value).toBe('PROMO')
    expect((wrapper.get('#affiliate_code').element as HTMLInputElement).value).toBe('AFF123')
    expect((wrapper.get('#password').element as HTMLInputElement).value).toBe('')
    expect((wrapper.get('#confirmPassword').element as HTMLInputElement).value).toBe('')
    expect(JSON.parse(sessionStorage.getItem('register_data')!)).toEqual({
      email: 'user@example.com', promo_code: 'PROMO', invitation_code: 'INVITE', aff_code: 'AFF123'
    })
  })

  it('discards the password handoff when navigation to verification is cancelled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings, turnstile_enabled: false, email_verify_enabled: true
    })
    pushMock.mockResolvedValueOnce({ type: 4 })
    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(takeRegistrationPassword('user@example.com')).toBe('')
    expect(sessionStorage.getItem('register_data')).not.toContain('password')
  })

  it('keeps the optional affiliate invitation field before Turnstile', async () => {
    const wrapper = mountRegister()
    await flushPromises()

    const invitationField = wrapper.get('[data-testid="affiliate-invitation-field"]')
    const turnstile = wrapper.get('[data-testid="registration-turnstile"]')

    expect(invitationField.get('input').attributes('id')).toBe('affiliate_code')
    expect(invitationField.text()).toContain('common.optional')
    expect(
      invitationField.element.compareDocumentPosition(turnstile.element) &
        Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
  })

  it('uses the mandatory invitation field without duplicating the affiliate field', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      invitation_code_enabled: true
    })

    const wrapper = mountRegister()
    await flushPromises()

    expect(wrapper.find('[data-testid="affiliate-invitation-field"]').exists()).toBe(false)
    expect(wrapper.get('#invitation_code').exists()).toBe(true)
  })

  it('submits a non-whitelist email domain so the backend can enforce its registration quota', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com'],
      registration_email_domain_quota_enabled: true
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('first@custom.example')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'first@custom.example' })
    )
    expect(showErrorMock).not.toHaveBeenCalled()
  })

  it('shows the localized registration domain quota message returned by the backend', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com'],
      registration_email_domain_quota_enabled: true
    })
    registerMock.mockRejectedValueOnce({
      reason: 'EMAIL_DOMAIN_REGISTRATION_LIMIT',
      message: 'raw backend message'
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('second@custom.example')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith(
      '该邮箱域名无法注册新账户。请使用主流邮箱注册；如需使用企业邮箱，请联系客服添加域名白名单。'
    )
  })

  // 域名限量注册开关默认关闭：恢复 PR5423 之前的客户端白名单预检，非白名单域名不发起注册请求。
  it('rejects a non-whitelist email domain locally when the domain quota switch is disabled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com']
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('first@custom.example')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).not.toHaveBeenCalled()
    // 校验失败通过 validationToastMessage watcher 弹 toast
    expect(showErrorMock).toHaveBeenCalledWith('auth.emailSuffixNotAllowedWithAllowed')
    expect(wrapper.get('#email').classes()).toContain('input-error')
  })

  it('still submits whitelisted email domains when the domain quota switch is disabled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com']
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@allowed.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'user@allowed.com' })
    )
    expect(showErrorMock).not.toHaveBeenCalled()
  })
})
