import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getActivePinia } from 'pinia'

import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId } from '@/types/adminAccess'

const { createAccountMock, importCodexSessionMock } = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  importCodexSessionMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isSimpleMode: false })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      probeUpstreamBilling: vi.fn().mockResolvedValue({}),
      syncUpstreamModels: vi.fn().mockResolvedValue({ models: [], metadata: {} }),
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      importCodexSession: importCodexSessionMock,
      createOpenAICodexPAT: vi.fn().mockResolvedValue({})
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
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([])
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: {
    showManualOption: Boolean,
    showCodexSessionImportOption: Boolean,
    showAgentIdentityOption: Boolean,
    showCodexPatOption: Boolean,
    initialInputMethod: String
  },
  data: () => ({ inputMethod: 'manual' }),
  emits: ['import-codex-session'],
  template: `<button type="button" data-testid="import-codex-session" @click="$emit('import-codex-session', 'session-json')">session</button>`
})

const ProxySelectorStub = defineComponent({
  name: 'ProxySelector',
  props: {
    modelValue: { type: [Number, null], default: null },
    proxies: { type: Array, default: () => [] }
  },
  emits: ['update:modelValue'],
  template: `<button type="button" data-testid="pick-proxy" @click="$emit('update:modelValue', 3)">proxy</button>`
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: { modelValue: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: `<button type="button" data-testid="select-groups" @click="$emit('update:modelValue', [1, 2])">groups</button>`
})

const ADDRESS_CREDENTIAL_KEYS = ['base_url', 'api_base_urls']

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

function mountModal() {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: ProxySelectorStub,
        GroupSelector: GroupSelectorStub,
        ModelWhitelistSelector: true,
        QuotaLimitCard: true
      }
    }
  })
}

async function selectButtonByText(wrapper: ReturnType<typeof mountModal>, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  expect(button).toBeDefined()
  await button?.trigger('click')
}

async function fillNameAndKey(wrapper: ReturnType<typeof mountModal>, name: string) {
  await wrapper.get('form#create-account-form input[type="text"]').setValue(name)
  await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
}

async function submit(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  await flushPromises()
}

describe('CreateAccountModal for operators', () => {
  beforeEach(() => {
    createAccountMock.mockReset().mockResolvedValue({ id: 42, platform: 'openai', type: 'apikey' })
    importCodexSessionMock.mockReset().mockResolvedValue({
      created: 1,
      updated: 0,
      skipped: 0,
      failed: 0,
      errors: [],
      warnings: []
    })
  })

  it('omits every address key and proxy_id and hides the upstream type without the upstream-address flag', async () => {
    seedOperator(['users.balance_adjust'])

    const antigravity = mountModal()
    await selectButtonByText(antigravity, 'Antigravity')
    expect(antigravity.find('[data-testid="antigravity-upstream-type"]').exists()).toBe(false)
    expect(antigravity.find('[data-testid="address-needs-flag"]').exists()).toBe(true)
    expect(antigravity.find('[data-testid="create-account-proxy"]').exists()).toBe(false)
    antigravity.unmount()

    // Kimi in adaptive mode: the platform default endpoints are used, mode and protocol stay.
    const kimi = mountModal()
    await selectButtonByText(kimi, 'Kimi')
    expect(kimi.find('[data-testid="create-access-address"]').exists()).toBe(false)
    expect(kimi.find('[data-testid="address-needs-flag"]').exists()).toBe(true)
    expect(kimi.text()).not.toContain('admin.accounts.cnProviders.apiProtocol.endpoints')
    expect(kimi.find('[data-testid="create-account-proxy"]').exists()).toBe(false)
    await fillNameAndKey(kimi, 'Kimi adaptive')
    await submit(kimi)

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const kimiPayload = createAccountMock.mock.calls[0][0]
    for (const key of ADDRESS_CREDENTIAL_KEYS) {
      expect(kimiPayload.credentials).not.toHaveProperty(key)
    }
    expect(kimiPayload.credentials).toMatchObject({ account_mode: 'payg', api_protocol: 'adaptive' })
    expect(kimiPayload).not.toHaveProperty('proxy_id')
    expect(kimiPayload.extra ?? {}).not.toHaveProperty('custom_base_url')
    expect(kimiPayload.extra ?? {}).not.toHaveProperty('custom_base_url_enabled')
    kimi.unmount()

    // The Codex session import goes through the same rules.
    const codex = mountModal()
    await selectButtonByText(codex, 'OpenAI')
    await codex.get('form#create-account-form input[type="text"]').setValue('Codex import')
    await submit(codex)
    await codex.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock).toHaveBeenCalledTimes(1)
    const importPayload = importCodexSessionMock.mock.calls[0][0]
    expect(importPayload).not.toHaveProperty('proxy_id')
    for (const key of ADDRESS_CREDENTIAL_KEYS) {
      expect(importPayload.credential_extras ?? {}).not.toHaveProperty(key)
    }
  })

  it('shows the group selector and sends group_ids with account management and no balance flag', async () => {
    seedOperator([])
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    expect(wrapper.find('[data-testid="select-groups"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="create-account-proxy"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="create-access-address"]').exists()).toBe(false)
    await fillNameAndKey(wrapper, 'grouped account')
    await wrapper.get('[data-testid="select-groups"]').trigger('click')
    await submit(wrapper)
    const payload = createAccountMock.mock.calls[0][0]
    expect(payload.group_ids).toEqual([1, 2])
    expect(payload).not.toHaveProperty('proxy_id')
    for (const key of ADDRESS_CREDENTIAL_KEYS) expect(payload.credentials).not.toHaveProperty(key)
    expect(payload.extra ?? {}).not.toHaveProperty('openai_long_context_billing_enabled')
  })

  it('keeps the address and proxy fields for a flag holder', async () => {
    seedOperator(['accounts.upstream_address', 'users.balance_adjust'])

    const antigravity = mountModal()
    await selectButtonByText(antigravity, 'Antigravity')
    expect(antigravity.find('[data-testid="antigravity-upstream-type"]').exists()).toBe(true)
    expect(antigravity.find('[data-testid="address-needs-flag"]').exists()).toBe(false)
    antigravity.unmount()

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    expect(wrapper.find('[data-testid="address-needs-flag"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-ungrouped-hint"]').exists()).toBe(false)
    await fillNameAndKey(wrapper, 'relay account')
    await wrapper.get('[data-testid="create-access-address"] input').setValue('https://relay.example.com/v1')
    await wrapper.get('[data-testid="create-account-proxy"] [data-testid="pick-proxy"]').trigger('click')
    await wrapper.get('[data-testid="select-groups"]').trigger('click')
    await submit(wrapper)

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0][0]
    expect(payload.credentials.base_url).toBe('https://relay.example.com/v1')
    expect(payload.proxy_id).toBe(3)
    expect(payload.group_ids).toEqual([1, 2])
  })
})
