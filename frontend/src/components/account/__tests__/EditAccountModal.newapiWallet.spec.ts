import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

// 「new-api 钱包余额（可选）」在编辑弹窗里的提交行为（xy-upstream-balance）。
// 单独成文件，避免改动 manual_merge 的 EditAccountModal.spec.ts；mock 与 stub 抄自该文件。

const { updateAccountMock, checkMixedChannelRiskMock } = vi.hoisted(() => ({
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: true })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([])
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('@/api/admin/groupCategories', () => ({
  default: { list: vi.fn(async () => []), save: vi.fn(), setGroupCategories: vi.fn() }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      te: () => false
    })
  }
})

import EditAccountModal from '../EditAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelMappings: { type: Array, default: () => [] },
    modelValue: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue'],
  template: '<div />'
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: { type: [String, Number, Boolean, null], default: '' },
    options: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue'],
  template: `
    <select v-bind="$attrs" :value="modelValue" @change="$emit('update:modelValue', $event.target.value)">
      <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
    </select>
  `
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: { modelValue: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: '<div data-testid="group-selector" />'
})

function buildRelayAccount() {
  return {
    id: 11,
    name: 'new-api relay',
    notes: '',
    platform: 'openai',
    type: 'apikey',
    credentials: {
      base_url: 'https://relay.example.com/v1',
      newapi_user_id: '42'
    },
    credentials_status: { has_api_key: true, has_newapi_access_token: true },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function mountModal(account = buildRelayAccount()) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Icon: true,
        ProxySelector: true,
        GroupSelector: GroupSelectorStub,
        ModelWhitelistSelector: ModelWhitelistSelectorStub
      }
    }
  })
}

async function submit(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('form#edit-account-form').trigger('submit.prevent')
  await flushPromises()
  expect(updateAccountMock).toHaveBeenCalledTimes(1)
  return updateAccountMock.mock.calls[0]?.[1]?.credentials as Record<string, unknown>
}

describe('EditAccountModal new-api wallet section', () => {
  beforeEach(() => {
    updateAccountMock.mockReset().mockImplementation(async (_id: number, payload: any) => ({ ...buildRelayAccount(), ...payload }))
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  it('keeps the new-api access token unless it is cleared, and sends null when cleared', async () => {
    const untouched = mountModal()
    await flushPromises()
    // 已配置：小节默认展开，显示「已配置」，令牌框是空的（后端已脱敏）。
    expect(untouched.get('[data-testid="newapi-wallet-configured"]').exists()).toBe(true)
    expect((untouched.get('[data-testid="newapi-wallet-token"]').element as HTMLInputElement).value).toBe('')
    const kept = await submit(untouched)
    expect(kept).not.toHaveProperty('newapi_access_token')
    expect(kept.newapi_user_id).toBe('42')
    untouched.unmount()

    updateAccountMock.mockClear()
    const cleared = mountModal()
    await flushPromises()
    await cleared.get('[data-testid="newapi-wallet-clear"]').trigger('click')
    const sent = await submit(cleared)
    expect(sent).toHaveProperty('newapi_access_token', null)
    expect(sent.newapi_user_id).toBe('42')
  })

  it('sends a newly typed new-api access token and removes an emptied user id', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="newapi-wallet-token"]').setValue(' tok ')
    await wrapper.get('[data-testid="newapi-wallet-user-id"]').setValue('')

    const sent = await submit(wrapper)
    expect(sent.newapi_access_token).toBe('tok')
    expect(sent).not.toHaveProperty('newapi_user_id')
  })

  it('starts collapsed for an account without new-api settings', async () => {
    const account = buildRelayAccount()
    account.credentials = { base_url: 'https://relay.example.com/v1' }
    account.credentials_status = { has_api_key: true }
    const wrapper = mountModal(account)
    await flushPromises()

    expect(wrapper.find('[data-testid="newapi-wallet-section"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="newapi-wallet-token"]').exists()).toBe(false)
    // 编辑弹窗里第一个密码框仍是 API Key。
    expect(wrapper.get('input[type="password"]').attributes('data-testid')).toBeUndefined()

    const sent = await submit(wrapper)
    expect(sent).not.toHaveProperty('newapi_access_token')
    expect(sent).not.toHaveProperty('newapi_user_id')
  })

  it.each([
    ['a non-numeric string', 'u42', 'u42'],
    ['a number beyond 20 digits', Number('123456789012345678901'), '123456789012345680000']
  ])('blocks saving a stored user id that is %s and shows the error on the field', async (_name, stored, shown) => {
    const account = buildRelayAccount()
    account.credentials = { base_url: 'https://relay.example.com/v1', newapi_user_id: stored }
    const wrapper = mountModal(account)
    await flushPromises()

    // 原值原样显示在输入框里，不会被悄悄丢掉。
    expect((wrapper.get('[data-testid="newapi-wallet-user-id"]').element as HTMLInputElement).value).toBe(shown)
    expect(wrapper.get('[data-testid="newapi-wallet-user-id-error"]').text()).toBe('admin.accounts.newapiWallet.userIdInvalid')

    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).not.toHaveBeenCalled()

    // 清空后可以保存，且删除该字段。
    await wrapper.get('[data-testid="newapi-wallet-user-id"]').setValue('')
    const sent = await submit(wrapper)
    expect(sent).not.toHaveProperty('newapi_user_id')
  })

  it('does not offer the new-api wallet section for antigravity accounts', async () => {
    const account = buildRelayAccount()
    account.platform = 'antigravity'
    account.credentials = { base_url: 'https://cloudcode-pa.googleapis.com' }
    account.credentials_status = { has_api_key: true }
    const wrapper = mountModal(account)
    await flushPromises()

    expect(wrapper.find('[data-testid="newapi-wallet-section"]').exists()).toBe(false)
  })
})
