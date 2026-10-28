import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId } from '@/types/adminAccess'

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
  useAuthStore: () => ({ isSimpleMode: false })
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
  getAntigravityDefaultModelMapping: vi.fn(),
  getIQCheck: vi.fn(),
  getIQCheckModels: vi.fn()
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

const ProxySelectorStub = defineComponent({
  name: 'ProxySelector',
  props: {
    modelValue: { type: [Number, null], default: null },
    proxies: { type: Array, default: () => [] },
    disabled: { type: Boolean, default: false }
  },
  emits: ['update:modelValue'],
  template: `<button type="button" data-testid="pick-proxy" :disabled="disabled" @click="$emit('update:modelValue', 3)">proxy</button>`
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: { modelValue: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: '<div data-testid="group-selector" />'
})

const NETWORK_CREDENTIAL_KEYS = ['base_url', 'api_base_urls', 'api_protocol', 'account_mode', 'aws_region']

function seedOperator(flags: AdminFlagId[]) {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { accounts: 'manage' },
    flags,
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

function baseAccount(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    name: 'Relay Key',
    notes: '',
    platform: 'openai',
    type: 'apikey',
    credentials: { model_mapping: { 'gpt-5.2': 'gpt-5.2' } },
    credentials_status: { has_api_key: true },
    extra: {},
    proxy_id: 4,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [11],
    expires_at: null,
    auto_pause_on_expired: false,
    ...overrides
  } as any
}

// A Kimi account in adaptive mode: the backend left out the addresses, mode and protocol stay.
function adaptiveKimiAccount() {
  return baseAccount({
    platform: 'kimi',
    credentials: { account_mode: 'payg', api_protocol: 'adaptive' }
  })
}

function anthropicOAuthAccount(overrides: Record<string, unknown> = {}) {
  return baseAccount({
    id: 2,
    name: 'Claude OAuth',
    platform: 'anthropic',
    type: 'oauth',
    credentials: { access_token: 'oauth-token' },
    credentials_status: {},
    ...overrides
  })
}

function mountModal(account: any) {
  return mount(EditAccountModal, {
    props: { show: true, account, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Icon: true,
        ProxySelector: ProxySelectorStub,
        GroupSelector: GroupSelectorStub,
        ModelWhitelistSelector: true
      }
    }
  })
}

async function submit(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('form#edit-account-form').trigger('submit.prevent')
  await flushPromises()
  expect(updateAccountMock).toHaveBeenCalledTimes(1)
  return updateAccountMock.mock.calls[0][1] as Record<string, any>
}

describe('EditAccountModal for operators: upstream address and proxy', () => {
  beforeEach(() => {
    updateAccountMock.mockReset().mockImplementation(async (id: number) => ({ id }))
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  it('hides the access address inputs without the upstream-address flag', async () => {
    seedOperator(['users.balance_adjust'])

    const apiKey = mountModal(baseAccount())
    await flushPromises()
    expect(apiKey.find('[data-testid="edit-access-address"]').exists()).toBe(false)
    expect(apiKey.find('[data-testid="edit-account-proxy"]').exists()).toBe(false)
    expect(apiKey.find('[data-testid="pick-proxy"]').exists()).toBe(false)
    apiKey.unmount()

    const adaptive = mountModal(adaptiveKimiAccount())
    await flushPromises()
    expect(adaptive.find('[data-testid="edit-access-address-endpoints"]').exists()).toBe(false)
    expect(adaptive.find('[data-testid="edit-access-address"]').exists()).toBe(false)
    adaptive.unmount()

    const upstream = mountModal(baseAccount({ platform: 'antigravity', type: 'upstream', credentials: {} }))
    await flushPromises()
    expect(upstream.find('[data-testid="edit-upstream-address"]').exists()).toBe(false)
    upstream.unmount()

    const oauth = mountModal(anthropicOAuthAccount())
    await flushPromises()
    expect(oauth.find('[data-testid="edit-custom-base-url"]').exists()).toBe(false)
    expect(oauth.find('[data-testid="edit-account-proxy"]').exists()).toBe(false)
    oauth.unmount()
  })

  it('never sends address keys or proxy_id for an operator without the flag', async () => {
    seedOperator(['users.balance_adjust'])

    const adaptive = mountModal(adaptiveKimiAccount())
    await flushPromises()
    const payload = await submit(adaptive)
    for (const key of NETWORK_CREDENTIAL_KEYS) {
      expect(payload.credentials).not.toHaveProperty(key)
    }
    expect(payload).not.toHaveProperty('proxy_id')
    expect(payload).not.toHaveProperty('type')
    // Fields outside the network identity are still saved.
    expect(payload.name).toBe('Relay Key')
    expect(payload.group_ids).toEqual([11])
    adaptive.unmount()

    updateAccountMock.mockClear()
    // Stale relay values on an OAuth account are dropped too.
    const oauth = mountModal(anthropicOAuthAccount({
      custom_base_url_enabled: true,
      custom_base_url: 'https://relay.example.com'
    }))
    await flushPromises()
    const oauthPayload = await submit(oauth)
    expect(oauthPayload).not.toHaveProperty('proxy_id')
    expect(oauthPayload.extra ?? {}).not.toHaveProperty('custom_base_url')
    expect(oauthPayload.extra ?? {}).not.toHaveProperty('custom_base_url_enabled')
  })

  it('lets a flag holder edit the address and proxy of an API-key account', async () => {
    seedOperator(['accounts.upstream_address', 'users.balance_adjust'])
    const wrapper = mountModal(baseAccount({
      credentials: { base_url: 'https://old.example.com/v1', model_mapping: { 'gpt-5.2': 'gpt-5.2' } }
    }))
    await flushPromises()

    const address = wrapper.get('[data-testid="edit-access-address"] input')
    expect((address.element as HTMLInputElement).value).toBe('https://old.example.com/v1')
    await address.setValue('https://new.example.com/v1')
    const proxy = wrapper.getComponent(ProxySelectorStub)
    expect(proxy.props('disabled')).toBe(false)
    await wrapper.get('[data-testid="pick-proxy"]').trigger('click')

    const payload = await submit(wrapper)
    expect(payload.credentials.base_url).toBe('https://new.example.com/v1')
    expect(payload.proxy_id).toBe(3)
  })

  it('shows address and proxy read-only on refreshable accounts even with the flag', async () => {
    seedOperator(['accounts.upstream_address', 'users.balance_adjust'])
    const wrapper = mountModal(anthropicOAuthAccount({
      custom_base_url_enabled: true,
      custom_base_url: 'https://relay.example.com'
    }))
    await flushPromises()

    const relay = wrapper.get('[data-testid="edit-custom-base-url"]')
    expect(relay.get('fieldset').attributes('disabled')).toBeDefined()
    expect(relay.text()).toContain('admin.operatorAccess.networkLockedRefreshable')
    const proxyBlock = wrapper.get('[data-testid="edit-account-proxy"]')
    expect(wrapper.getComponent(ProxySelectorStub).props('disabled')).toBe(true)
    expect(proxyBlock.text()).toContain('admin.operatorAccess.networkLockedRefreshable')

    const payload = await submit(wrapper)
    expect(payload).not.toHaveProperty('proxy_id')
    expect(payload).not.toHaveProperty('type')
    expect(payload.extra ?? {}).not.toHaveProperty('custom_base_url')
    expect(payload.extra ?? {}).not.toHaveProperty('custom_base_url_enabled')
  })

  it('hides the group selector and billing extras without the balance flag', async () => {
    seedOperator(['accounts.upstream_address'])

    const openai = mountModal(baseAccount({ extra: { openai_long_context_billing_enabled: true } }))
    await flushPromises()
    expect(openai.find('[data-testid="group-selector"]').exists()).toBe(false)
    expect(openai.find('[data-testid="openai-long-context-billing-toggle"]').exists()).toBe(false)
    const payload = await submit(openai)
    expect(payload).not.toHaveProperty('group_ids')
    expect(payload.extra ?? {}).not.toHaveProperty('openai_long_context_billing_enabled')
    openai.unmount()

    updateAccountMock.mockClear()
    const anthropic = mountModal(anthropicOAuthAccount({
      cache_ttl_override_enabled: true,
      cache_ttl_override_target: '1h'
    }))
    await flushPromises()
    expect(anthropic.text()).not.toContain('admin.accounts.quotaControl.cacheTTLOverride.label')
    const anthropicPayload = await submit(anthropic)
    expect(anthropicPayload).not.toHaveProperty('group_ids')
    expect(anthropicPayload.extra ?? {}).not.toHaveProperty('cache_ttl_override_enabled')
    expect(anthropicPayload.extra ?? {}).not.toHaveProperty('cache_ttl_override_target')
  })
})


it.each(['openai', 'kimi', 'grok'])('preserves masked userinfo byte for byte on a %s API-key account', async (platform) => {
  seedOperator(['accounts.upstream_address', 'users.balance_adjust'])
  updateAccountMock.mockClear()
  const base_url = 'https://***@relay.example.com:8443/v1/'
  const wrapper = mountModal(baseAccount({ platform, credentials: { base_url } }))
  await flushPromises()
  const payload = await submit(wrapper)
  expect(payload.credentials.base_url).toBe(base_url)
  wrapper.unmount()
})
