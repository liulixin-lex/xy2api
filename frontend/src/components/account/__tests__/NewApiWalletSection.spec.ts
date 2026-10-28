import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import NewApiWalletSection from '../NewApiWalletSection.vue'
import {
  applyNewApiWalletCredentials,
  emptyNewApiWalletDraft,
  isNewApiWalletUserIdValid,
  newApiWalletDraftFromCredentials,
  type NewApiWalletDraft
} from '../newApiWalletCredentials'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

// 用一个真实的 v-model 宿主挂载，便于断言草稿的变化。
function mountSection(initial: Partial<NewApiWalletDraft> = {}, configured = false) {
  const wrapper = mount(NewApiWalletSection, {
    props: {
      configured,
      modelValue: { ...emptyNewApiWalletDraft(), ...initial },
      'onUpdate:modelValue': (value: NewApiWalletDraft) => wrapper.setProps({ modelValue: value })
    }
  })
  return wrapper
}

const lastDraft = (wrapper: ReturnType<typeof mountSection>) => {
  const events = wrapper.emitted('update:modelValue') ?? []
  return events[events.length - 1]?.[0] as NewApiWalletDraft
}

describe('NewApiWalletSection', () => {
  it('starts collapsed and shows the token and user id fields after a click', async () => {
    const wrapper = mountSection()
    expect(wrapper.find('[data-testid="newapi-wallet-token"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="newapi-wallet-user-id"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="newapi-wallet-toggle"]').attributes('aria-expanded')).toBe('false')

    await wrapper.get('[data-testid="newapi-wallet-toggle"]').trigger('click')

    const token = wrapper.get('[data-testid="newapi-wallet-token"]')
    expect(token.attributes('type')).toBe('password')
    expect(token.attributes('autocomplete')).toBe('new-password')
    expect(token.attributes('data-lpignore')).toBe('true')
    expect(wrapper.find('[data-testid="newapi-wallet-user-id"]').exists()).toBe(true)
    // 未配置时没有「已配置」行，也没有改地址会清除令牌的提示。
    expect(wrapper.find('[data-testid="newapi-wallet-configured"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="newapi-wallet-clear"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="newapi-wallet-host-change-hint"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.newapiWallet.hint')

    await token.setValue('tok')
    expect(lastDraft(wrapper)).toEqual({ accessToken: 'tok', userId: '', clearToken: false })
  })

  it('starts expanded when a user id is already stored', () => {
    const wrapper = mountSection({ userId: '42' })
    expect((wrapper.get('[data-testid="newapi-wallet-user-id"]').element as HTMLInputElement).value).toBe('42')
  })

  it('shows the configured tag and lets the clear button be undone', async () => {
    const wrapper = mountSection({}, true)
    // 已配置时默认展开。
    expect(wrapper.get('[data-testid="newapi-wallet-configured"]').text()).toBe('admin.accounts.newapiWallet.configured')
    expect(wrapper.get('[data-testid="newapi-wallet-host-change-hint"]').text()).toBe(
      'admin.accounts.newapiWallet.hostChangeHint'
    )
    expect(wrapper.get('[data-testid="newapi-wallet-token"]').attributes('placeholder')).toBe(
      'admin.accounts.newapiWallet.tokenKeepPlaceholder'
    )
    const clear = wrapper.get('[data-testid="newapi-wallet-clear"]')
    expect(clear.text()).toBe('admin.accounts.newapiWallet.clear')

    await wrapper.get('[data-testid="newapi-wallet-token"]').setValue('typed')
    await clear.trigger('click')
    expect(lastDraft(wrapper)).toEqual({ accessToken: '', userId: '', clearToken: true })
    expect(wrapper.find('[data-testid="newapi-wallet-configured"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="newapi-wallet-clear-pending"]').text()).toBe('admin.accounts.newapiWallet.clearPending')
    expect(wrapper.get('[data-testid="newapi-wallet-token"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="newapi-wallet-clear"]').text()).toBe('admin.accounts.newapiWallet.undoClear')

    await wrapper.get('[data-testid="newapi-wallet-clear"]').trigger('click')
    expect(lastDraft(wrapper)).toEqual({ accessToken: '', userId: '', clearToken: false })
    expect(wrapper.find('[data-testid="newapi-wallet-configured"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="newapi-wallet-token"]').attributes('disabled')).toBeUndefined()
  })

  it('shows what will be saved in the header while collapsed', async () => {
    const wrapper = mountSection()
    await wrapper.get('[data-testid="newapi-wallet-toggle"]').trigger('click')
    await wrapper.get('[data-testid="newapi-wallet-token"]').setValue('tok')
    // 展开时标题旁不显示状态。
    expect(wrapper.find('[data-testid="newapi-wallet-collapsed-status"]').exists()).toBe(false)

    await wrapper.get('[data-testid="newapi-wallet-toggle"]').trigger('click')
    expect(wrapper.find('[data-testid="newapi-wallet-token"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="newapi-wallet-collapsed-status"]').text()).toBe('admin.accounts.newapiWallet.tokenFilled')

    const configured = mountSection({}, true)
    await configured.get('[data-testid="newapi-wallet-toggle"]').trigger('click')
    expect(configured.get('[data-testid="newapi-wallet-collapsed-status"]').text()).toBe('admin.accounts.newapiWallet.configured')

    const clearing = mountSection({ clearToken: true }, true)
    await clearing.get('[data-testid="newapi-wallet-toggle"]').trigger('click')
    expect(clearing.get('[data-testid="newapi-wallet-collapsed-status"]').text()).toBe('admin.accounts.newapiWallet.clearPending')

    // 未配置也没填写：折叠时不显示状态。
    expect(mountSection().find('[data-testid="newapi-wallet-collapsed-status"]').exists()).toBe(false)
  })

  it('flags a stored user id that is not 1–20 digits', async () => {
    const wrapper = mountSection({ userId: 'u42' })
    expect(wrapper.get('[data-testid="newapi-wallet-user-id-error"]').text()).toBe('admin.accounts.newapiWallet.userIdInvalid')
    expect((wrapper.get('[data-testid="newapi-wallet-user-id"]').element as HTMLInputElement).value).toBe('u42')

    await wrapper.get('[data-testid="newapi-wallet-toggle"]').trigger('click')
    expect(wrapper.get('[data-testid="newapi-wallet-collapsed-status"]').text()).toBe('admin.accounts.newapiWallet.userIdInvalid')

    // 重新输入时只保留数字，错误随之消失。
    await wrapper.get('[data-testid="newapi-wallet-toggle"]').trigger('click')
    await wrapper.get('[data-testid="newapi-wallet-user-id"]').setValue('u42')
    expect(lastDraft(wrapper).userId).toBe('42')
    expect(wrapper.find('[data-testid="newapi-wallet-user-id-error"]').exists()).toBe(false)
  })

  it('keeps only digits in the user id', async () => {
    const wrapper = mountSection()
    await wrapper.get('[data-testid="newapi-wallet-toggle"]').trigger('click')

    const input = wrapper.get('[data-testid="newapi-wallet-user-id"]')
    await input.setValue('12a3')

    expect(lastDraft(wrapper).userId).toBe('123')
    expect((input.element as HTMLInputElement).value).toBe('123')
  })
})

describe('newApiWalletCredentials', () => {
  it('omits an untouched token, sends null to clear it and a string to replace it', () => {
    const base = () => ({ base_url: 'https://relay.example.com/v1', newapi_user_id: '42' }) as Record<string, unknown>
    const cases: Array<{ name: string; draft: NewApiWalletDraft; mode: 'create' | 'edit'; token: unknown; userId?: string }> = [
      { name: 'edit untouched', draft: { accessToken: '', userId: '42', clearToken: false }, mode: 'edit', token: undefined, userId: '42' },
      { name: 'edit cleared', draft: { accessToken: '', userId: '42', clearToken: true }, mode: 'edit', token: null, userId: '42' },
      { name: 'edit replaced', draft: { accessToken: ' tok ', userId: '42', clearToken: false }, mode: 'edit', token: 'tok', userId: '42' },
      { name: 'edit whitespace only', draft: { accessToken: '   ', userId: '42', clearToken: false }, mode: 'edit', token: undefined, userId: '42' },
      { name: 'create empty', draft: { accessToken: '', userId: '', clearToken: false }, mode: 'create', token: undefined },
      // 创建时忽略「清除」，永远不会发送 null。
      { name: 'create ignores clear', draft: { accessToken: '', userId: '', clearToken: true }, mode: 'create', token: undefined },
      { name: 'create filled', draft: { accessToken: 'tok', userId: '7', clearToken: false }, mode: 'create', token: 'tok', userId: '7' }
    ]
    for (const c of cases) {
      const credentials = base()
      applyNewApiWalletCredentials(credentials, c.draft, c.mode)
      if (c.token === undefined) {
        expect(credentials, c.name).not.toHaveProperty('newapi_access_token')
      } else {
        expect(credentials, c.name).toHaveProperty('newapi_access_token', c.token)
      }
      if (c.userId === undefined) {
        expect(credentials, c.name).not.toHaveProperty('newapi_user_id')
      } else {
        expect(credentials.newapi_user_id, c.name).toBe(c.userId)
      }
      expect(credentials.base_url, c.name).toBe('https://relay.example.com/v1')
    }

    // 脱敏后的 credentials 里若带着 newapi_access_token: null，未改动时也不回传。
    const redacted: Record<string, unknown> = { newapi_access_token: null }
    applyNewApiWalletCredentials(redacted, emptyNewApiWalletDraft(), 'edit')
    expect(redacted).not.toHaveProperty('newapi_access_token')
  })

  it('reads the stored user id and drops it when emptied', () => {
    expect(newApiWalletDraftFromCredentials()).toEqual(emptyNewApiWalletDraft())
    expect(newApiWalletDraftFromCredentials(null)).toEqual(emptyNewApiWalletDraft())
    expect(newApiWalletDraftFromCredentials({ newapi_user_id: '42' })).toEqual({ accessToken: '', userId: '42', clearToken: false })
    expect(newApiWalletDraftFromCredentials({ newapi_user_id: 42 }).userId).toBe('42')
    // 已存的值原样保留（不合法的也不悄悄丢掉），由提交前校验拦下。
    expect(newApiWalletDraftFromCredentials({ newapi_user_id: 'u42' }).userId).toBe('u42')
    expect(newApiWalletDraftFromCredentials({ newapi_user_id: -1 }).userId).toBe('-1')
    expect(newApiWalletDraftFromCredentials({ newapi_user_id: 1.5 }).userId).toBe('1.5')
    expect(newApiWalletDraftFromCredentials({ newapi_user_id: Number('123456789012345678901') }).userId).toBe('123456789012345680000')
    // 令牌已脱敏，永远不会回填。
    expect(newApiWalletDraftFromCredentials({ newapi_access_token: 'secret' }).accessToken).toBe('')

    expect(isNewApiWalletUserIdValid('')).toBe(true)
    expect(isNewApiWalletUserIdValid('42')).toBe(true)
    expect(isNewApiWalletUserIdValid(' 42 ')).toBe(true)
    expect(isNewApiWalletUserIdValid('1'.repeat(20))).toBe(true)
    expect(isNewApiWalletUserIdValid('1'.repeat(21))).toBe(false)
    expect(isNewApiWalletUserIdValid('u42')).toBe(false)
    expect(isNewApiWalletUserIdValid('-1')).toBe(false)
    expect(isNewApiWalletUserIdValid('1.5')).toBe(false)
    expect(isNewApiWalletUserIdValid('1.2345678901234568e+21')).toBe(false)

    const credentials: Record<string, unknown> = { newapi_user_id: '42' }
    applyNewApiWalletCredentials(credentials, { accessToken: '', userId: '', clearToken: false }, 'edit')
    expect(credentials).not.toHaveProperty('newapi_user_id')
  })
})
