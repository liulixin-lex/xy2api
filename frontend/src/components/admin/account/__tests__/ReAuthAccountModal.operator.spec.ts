import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

import { useAdminAccessStore } from '@/stores/adminAccess'

const { exchangeCodeMock, applyOAuthCredentialsMock } = vi.hoisted(() => ({
  exchangeCodeMock: vi.fn(),
  applyOAuthCredentialsMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      exchangeCode: exchangeCodeMock,
      applyOAuthCredentials: applyOAuthCredentialsMock,
      update: vi.fn(),
      clearError: vi.fn()
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import ReAuthAccountModal from '../ReAuthAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: { addMethod: { type: String, default: '' } },
  emits: ['cookie-auth'],
  data: () => ({ inputMethod: 'cookie', authCode: '', oauthState: '', projectId: '', sessionKey: '' }),
  methods: { reset() {} },
  template: `<button type="button" data-testid="cookie-auth" @click="$emit('cookie-auth', 'session-key')">cookie</button>`
})

function signIn(role: 'admin' | 'operator') {
  getActivePinia()!.state.value.auth = { user: { id: 7, role }, runMode: 'standard' }
  if (role !== 'operator') return
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { accounts: 'manage' },
    flags: ['accounts.upstream_address'],
    settings: {
      ops_monitoring_enabled: false,
      ops_realtime_monitoring_enabled: false,
      ops_query_mode_default: 'auto',
      admin_usage_cache_hit_rate_enabled: false,
      admin_usage_token_speed_enabled: false,
      payment_enabled: false,
      account_quota_notify_enabled: false,
      web_search_emulation_enabled: false,
      payment_subscription_usd_to_cny_rate: 0,
      payment_recharge_fee_rate: 0
    },
    custom_menu_items: []
  }
  access.loaded = true
}

function claudeAccount(type: 'oauth' | 'setup-token') {
  return {
    id: 9,
    name: 'Claude account',
    platform: 'anthropic',
    type,
    proxy_id: 4,
    credentials: {},
    extra: {
      custom_base_url_enabled: true,
      custom_base_url: 'https://relay.example.com',
      org_uuid: 'org-old'
    },
    status: 'active'
  } as any
}

async function mountShown(account: any) {
  const wrapper = mount(ReAuthAccountModal, {
    props: { show: false, account },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        Icon: true
      }
    }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('ReAuthAccountModal for operators', () => {
  beforeEach(() => {
    exchangeCodeMock.mockReset().mockResolvedValue({
      access_token: 'new-access-token',
      org_uuid: 'org-1',
      account_uuid: 'acc-1',
      email_address: 'owner@example.com'
    })
    applyOAuthCredentialsMock.mockReset().mockImplementation(async (id: number) => ({ id }))
  })

  it('never sends custom_base_url in extra', async () => {
    signIn('operator')
    const wrapper = await mountShown(claudeAccount('oauth'))

    await wrapper.get('[data-testid="cookie-auth"]').trigger('click')
    await flushPromises()

    expect(applyOAuthCredentialsMock).toHaveBeenCalledTimes(1)
    const [id, body] = applyOAuthCredentialsMock.mock.calls[0]
    expect(id).toBe(9)
    expect(body.extra).toEqual({ org_uuid: 'org-1', account_uuid: 'acc-1', email_address: 'owner@example.com' })
    expect(body.extra).not.toHaveProperty('custom_base_url')
    expect(body.extra).not.toHaveProperty('custom_base_url_enabled')
    expect(body).not.toHaveProperty('proxy_id')
  })

  it('keeps the stored method for operators', async () => {
    signIn('operator')
    const wrapper = await mountShown(claudeAccount('setup-token'))

    const fieldset = wrapper.get('fieldset')
    expect(fieldset.attributes('disabled')).toBeDefined()
    const checked = wrapper.findAll('input[type="radio"]').find(radio => (radio.element as HTMLInputElement).checked)
    expect(checked?.attributes('value')).toBe('setup-token')
    expect(wrapper.getComponent(OAuthAuthorizationFlowStub).props('addMethod')).toBe('setup-token')

    await wrapper.get('[data-testid="cookie-auth"]').trigger('click')
    await flushPromises()

    expect(exchangeCodeMock).toHaveBeenCalledWith('/admin/accounts/setup-token-cookie-auth', expect.objectContaining({ proxy_id: 4 }))
    expect(applyOAuthCredentialsMock).toHaveBeenCalledWith(9, expect.objectContaining({ type: 'setup-token' }))
    wrapper.unmount()

    // A super admin can still switch the method.
    signIn('admin')
    const adminView = await mountShown(claudeAccount('setup-token'))
    expect(adminView.get('fieldset').attributes('disabled')).toBeUndefined()
  })
})
