import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Toggle from '@/components/common/Toggle.vue'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId, AdminLevel, MeAccess } from '@/types/adminAccess'
import ChannelsView from '@/views/admin/ChannelsView.vue'

const { listChannels, updateChannel, getGroups } = vi.hoisted(() => ({
  listChannels: vi.fn(),
  updateChannel: vi.fn(),
  getGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: { list: listChannels, update: updateChannel },
    groups: { getAll: getGroups },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false }) }
  }
}))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

function seedOperator(level: AdminLevel, flags: AdminFlagId[]) {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { channels: level },
    flags,
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

function mountView() {
  return mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: defineComponent({ template: '<main><slot /></main>' }),
        TablePageLayout: defineComponent({
          template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>'
        }),
        DataTable: defineComponent({
          props: ['data'],
          template: '<div><div v-for="row in data" :key="row.id" :data-test="`channel-${row.id}`"><slot name="cell-status" :row="row" /><slot name="cell-actions" :row="row" /></div></div>'
        }),
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        Icon: true,
        PlatformIcon: true
      }
    }
  })
}

const toggleOf = (wrapper: ReturnType<typeof mountView>, id: number) =>
  wrapper.get(`[data-test="channel-${id}"]`).getComponent(Toggle)

describe('ChannelsView status toggle for operators', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    getGroups.mockResolvedValue([])
    updateChannel.mockResolvedValue({})
    listChannels.mockResolvedValue({
      items: [
        { id: 1, name: 'grouped', status: 'active', group_ids: [3], model_pricing: [] },
        { id: 2, name: 'ungrouped', status: 'active', group_ids: [], model_pricing: [] }
      ],
      total: 2
    })
  })

  it('locks the toggle of a grouped channel without "Adjust balance"', async () => {
    seedOperator('manage', [])
    const wrapper = mountView()
    await flushPromises()

    expect(toggleOf(wrapper, 1).attributes('disabled')).toBeDefined()
    toggleOf(wrapper, 1).vm.$emit('update:modelValue', false)
    await flushPromises()
    expect(updateChannel).not.toHaveBeenCalled()

    expect(toggleOf(wrapper, 2).attributes('disabled')).toBeUndefined()
    await toggleOf(wrapper, 2).trigger('click')
    await flushPromises()
    expect(updateChannel).toHaveBeenCalledWith(2, { status: 'disabled' })
    wrapper.unmount()
  })

  it('unlocks grouped channels with "Adjust balance" and locks every toggle for view-only access', async () => {
    seedOperator('manage', ['users.balance_adjust'])
    let wrapper = mountView()
    await flushPromises()
    expect(toggleOf(wrapper, 1).attributes('disabled')).toBeUndefined()
    wrapper.unmount()

    seedOperator('view', ['users.balance_adjust'])
    wrapper = mountView()
    await flushPromises()
    for (const id of [1, 2]) {
      expect(toggleOf(wrapper, id).attributes('disabled')).toBeDefined()
      toggleOf(wrapper, id).vm.$emit('update:modelValue', false)
    }
    await flushPromises()
    expect(updateChannel).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})


it.each([false, true, 'admin'] as const)('locks grouped account-stats pricing and omits unchanged values without the flag (%s)', async (flag) => {
  seedOperator('manage', flag ? ['users.balance_adjust'] : [])
  if (flag === 'admin') getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' } }
  getGroups.mockResolvedValue([{ id: 3, name: 'Group', platform: 'openai' }])
  listChannels.mockResolvedValue({ items: [{ id: 1, name: 'grouped', status: 'active', group_ids: [3], model_pricing: [], account_stats_pricing_rules: [], apply_pricing_to_account_stats: true }], total: 1 })
  const wrapper = mountView()
  await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
  await flushPromises()
  expect(wrapper.get('[data-testid="account-stats-pricing"]').attributes('disabled') !== undefined).toBe(!flag)
  const pricingToggle = wrapper.findAllComponents(Toggle).find(toggle => toggle.props('modelValue') === true && toggle.attributes('disabled') !== undefined)
  if (!flag) expect(pricingToggle).toBeDefined()
  await wrapper.get('#channel-form').trigger('submit')
  await flushPromises()
  const payload = updateChannel.mock.calls.at(-1)![1]
  expect(Object.hasOwn(payload, 'apply_pricing_to_account_stats')).toBe(!!flag)
  expect(Object.hasOwn(payload, 'account_stats_pricing_rules')).toBe(!!flag)
  wrapper.unmount()
})
