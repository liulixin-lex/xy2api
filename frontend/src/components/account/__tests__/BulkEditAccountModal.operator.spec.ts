import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

import BulkEditAccountModal from '../BulkEditAccountModal.vue'
import { adminAPI } from '@/api/admin'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId } from '@/types/adminAccess'

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
      bulkUpdate: vi.fn(),
      checkMixedChannelRisk: vi.fn()
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
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
  return mount(BulkEditAccountModal, {
    props: {
      show: true,
      accountIds: [1, 2],
      selectedPlatforms: ['openai'],
      selectedTypes: ['apikey'],
      proxies: [],
      groups: []
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        Select: true,
        ProxySelector: true,
        GroupSelector: {
          props: ['modelValue'], emits: ['update:modelValue'],
          template: `<button type="button" data-testid="pick-groups" @click="$emit('update:modelValue', [11])">groups</button>`
        },
        Icon: true
      }
    }
  })
}

describe('BulkEditAccountModal for operators', () => {
  beforeEach(() => {
    vi.mocked(adminAPI.accounts.bulkUpdate).mockReset().mockResolvedValue({ success: 2, failed: 0, results: [] } as any)
    vi.mocked(adminAPI.accounts.checkMixedChannelRisk).mockReset().mockResolvedValue({ has_risk: false } as any)
  })

  it('sends group_ids but never proxy_id or base_url for an operator without the flags', async () => {
    seedOperator([])
    const wrapper = mountModal()
    await flushPromises()

    expect(wrapper.find('[data-testid="bulk-edit-base-url-section"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="bulk-edit-proxy-section"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="bulk-edit-groups-section"]').exists()).toBe(true)
    expect(wrapper.find('#bulk-edit-openai-long-context-billing-enabled').exists()).toBe(false)

    await wrapper.get('#bulk-edit-priority-enabled').setValue(true)
    await wrapper.get('#bulk-edit-priority').setValue(5)
    // Hidden network fields are stripped while the visible group selector is saved.
    const vm = wrapper.vm as any
    vm.enableBaseUrl = true
    vm.baseUrl = 'https://relay.example.com/v1'
    vm.enableProxy = true
    vm.proxyId = 3
    await wrapper.get('#bulk-edit-groups-enabled').setValue(true)
    await wrapper.get('[data-testid="pick-groups"]').trigger('click')
    await wrapper.get('#bulk-edit-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(adminAPI.accounts.bulkUpdate).toHaveBeenCalledTimes(1)
    const [ids, payload] = vi.mocked(adminAPI.accounts.bulkUpdate).mock.calls[0] as unknown as [number[], Record<string, any>]
    expect(ids).toEqual([1, 2])
    expect(payload.priority).toBe(5)
    expect(payload).not.toHaveProperty('proxy_id')
    expect(payload.group_ids).toEqual([11])
    expect(payload).not.toHaveProperty('base_url')
    expect(payload.credentials ?? {}).not.toHaveProperty('base_url')
    wrapper.unmount()

    // The upstream-address flag does not open bulk network edits.
    seedOperator(['accounts.upstream_address', 'users.balance_adjust'])
    const flagHolder = mountModal()
    await flushPromises()
    expect(flagHolder.find('[data-testid="bulk-edit-base-url-section"]').exists()).toBe(false)
    expect(flagHolder.find('[data-testid="bulk-edit-proxy-section"]').exists()).toBe(false)
    expect(flagHolder.find('[data-testid="bulk-edit-groups-section"]').exists()).toBe(true)
    flagHolder.unmount()
  })
})
