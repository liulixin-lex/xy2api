import { defineComponent, nextTick } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ChannelMonitor } from '@/api/admin/channelMonitor'
import Select from '@/components/common/Select.vue'
import MonitorFormDialog from '@/components/admin/monitor/MonitorFormDialog.vue'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId, AdminLevel, AdminModuleId, MeAccess } from '@/types/adminAccess'

const { listTemplates, accountsList, accountsGetById, monitorCreate, monitorUpdate } = vi.hoisted(() => ({
  listTemplates: vi.fn(),
  accountsList: vi.fn(),
  accountsGetById: vi.fn(),
  monitorCreate: vi.fn(),
  monitorUpdate: vi.fn()
}))

vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorV1Mode: () => true,
  isChannelMonitorV2Mode: () => false,
  getChannelMonitorMode: () => 'v1' as const
}))

vi.mock('@/features/channel-monitor-v2/MonitorSettingsPanel.vue', () => ({
  default: { name: 'MonitorSettingsPanel', template: '<div />' }
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channelMonitor: { create: monitorCreate, update: monitorUpdate },
    channelMonitorTemplate: { list: listTemplates },
    accounts: {
      list: (...args: unknown[]) => accountsList(...args),
      getById: (...args: unknown[]) => accountsGetById(...args)
    }
  }
}))

vi.mock('@/api/keys', () => ({ keysAPI: { list: vi.fn() } }))
vi.mock('@/api/groups', () => ({ userGroupsAPI: { getUserGroupRates: vi.fn() } }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null, showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const BaseDialogStub = defineComponent({
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

function makeMonitor(overrides: Partial<ChannelMonitor> = {}): ChannelMonitor {
  return {
    id: 42,
    name: 'primary',
    provider: 'anthropic',
    api_mode: 'chat_completions',
    endpoint: 'https://api.example.com',
    api_key_masked: 'sk-t***',
    primary_model: 'claude-sonnet-4-5',
    extra_models: [],
    group_name: '',
    enabled: true,
    interval_seconds: 60,
    jitter_seconds: 0,
    last_checked_at: null,
    created_by: 1,
    created_at: '2026-07-16T00:00:00Z',
    updated_at: '2026-07-16T00:00:00Z',
    primary_status: '',
    primary_latency_ms: null,
    availability_7d: 0,
    extra_models_status: [],
    template_id: null,
    extra_headers: {},
    body_override_mode: 'off',
    body_override: null,
    check_mode: 'probe',
    account_id: null,
    ...overrides
  }
}

const template = (id: number, name: string, extra_headers: Record<string, string>) => ({
  id,
  name,
  provider: 'anthropic',
  api_mode: 'chat_completions',
  description: '',
  extra_headers,
  body_override_mode: 'off',
  body_override: null,
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
  associated_monitors: 0
})

function seedOperator(modules: Partial<Record<AdminModuleId, AdminLevel>>, flags: AdminFlagId[] = []) {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules,
    flags,
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

let current: VueWrapper | undefined

function mountDialog(monitor: ChannelMonitor | null = null) {
  current = mount(MonitorFormDialog, {
    props: { show: true, monitor },
    global: {
      plugins: [getActivePinia()!],
      stubs: {
        BaseDialog: BaseDialogStub,
        Toggle: true,
        ModelTagInput: true,
        MonitorKeyPickerDialog: true,
        MonitorAdvancedRequestConfig: true
      }
    }
  })
  return current
}

const templateSelect = (wrapper: VueWrapper) =>
  wrapper.findAllComponents(Select).find((select) =>
    (select.props('options') as Array<{ label: string }>).some((option) => option.label === 'admin.channelMonitor.templateField.none')
  )!

async function templateOptionStates(wrapper: VueWrapper) {
  await templateSelect(wrapper).get('button.select-trigger').trigger('click')
  await nextTick()
  const options = [...document.body.querySelectorAll<HTMLElement>('.select-dropdown-portal .select-option')]
  return Object.fromEntries(options.map((option) => [option.textContent?.trim(), option.getAttribute('aria-disabled')]))
}

const submit = async (wrapper: VueWrapper) => {
  await wrapper.get('#channel-monitor-form').trigger('submit')
  await flushPromises()
}

beforeEach(() => {
  listTemplates.mockReset().mockResolvedValue({
    items: [template(1, 'plain', {}), template(2, 'with headers', { 'X-Upstream-Token': 'secret' })]
  })
  accountsList.mockReset().mockResolvedValue({ items: [] })
  accountsGetById.mockReset()
  monitorCreate.mockReset().mockResolvedValue({})
  monitorUpdate.mockReset().mockResolvedValue({})
})

afterEach(() => {
  current?.unmount()
  current = undefined
  document.body.innerHTML = ''
})

describe('MonitorFormDialog for operators', () => {
  it('does not let an operator pick a template with request headers', async () => {
    seedOperator({ accounts: 'view' }, ['accounts.upstream_address'])
    const wrapper = mountDialog(makeMonitor())
    await flushPromises()

    expect(wrapper.find('[data-testid="monitor-template-super-only"]').text()).toBe('admin.operatorAccess.templateApplySuperOnly')
    expect(await templateOptionStates(wrapper)).toMatchObject({ plain: 'false', 'with headers': 'true' })

    // Even a forced pick of the headered template is ignored.
    templateSelect(wrapper).vm.$emit('update:modelValue', '2')
    await nextTick()
    expect(templateSelect(wrapper).props('modelValue')).toBe('')

    templateSelect(wrapper).vm.$emit('update:modelValue', '1')
    await nextTick()
    expect(templateSelect(wrapper).props('modelValue')).toBe('1')
  })

  it('lets a super admin pick any template', async () => {
    getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }
    const wrapper = mountDialog(makeMonitor())
    await flushPromises()

    expect(wrapper.find('[data-testid="monitor-template-super-only"]').exists()).toBe(false)
    expect(await templateOptionStates(wrapper)).toMatchObject({ plain: 'false', 'with headers': 'false' })
  })

  it('keeps an existing headered template without sending template_id again', async () => {
    seedOperator({ accounts: 'view' }, ['accounts.upstream_address'])
    const wrapper = mountDialog(makeMonitor({ template_id: 2 }))
    await flushPromises()

    await submit(wrapper)
    expect(monitorUpdate).toHaveBeenCalledTimes(1)
    const payload = monitorUpdate.mock.calls[0][1] as Record<string, unknown>
    expect(payload).not.toHaveProperty('template_id')
    expect(payload).not.toHaveProperty('clear_template')
  })

  it('keeps the linked account read-only without accounts:view', async () => {
    seedOperator({}, ['accounts.upstream_address'])
    const wrapper = mountDialog(makeMonitor({
      check_mode: 'quota',
      account_id: 999,
      endpoint: '',
      primary_model: 'quota'
    }))
    await flushPromises()

    expect(accountsList).not.toHaveBeenCalled()
    expect(accountsGetById).not.toHaveBeenCalled()
    const trigger = wrapper.get('[data-testid="monitor-linked-account"] button')
    expect(trigger.attributes('disabled')).toBeDefined()
    expect(trigger.text()).toContain('#999')
    expect(wrapper.text()).not.toContain('admin.channelMonitor.form.linkedAccountEmpty')

    await submit(wrapper)
    expect(monitorUpdate).toHaveBeenCalledTimes(1)
    expect(monitorUpdate.mock.calls[0][1]).not.toHaveProperty('account_id')
  })

  it('offers no quota mode on a monitor without a linked account to an operator without accounts:view', async () => {
    seedOperator({}, ['accounts.upstream_address'])
    const wrapper = mountDialog(makeMonitor())
    await flushPromises()

    expect(wrapper.get('[data-testid="monitor-check-mode-quota"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="monitor-check-mode-quota_probe"]').attributes('disabled')).toBeDefined()
    expect(accountsList).not.toHaveBeenCalled()
  })
})
