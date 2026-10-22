import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountGroupsDialog from '../AccountGroupsDialog.vue'
import type { AccountListItem, AdminGroup } from '@/types'

const { getById, updateAccount, showError, showSuccess, categoriesApi } = vi.hoisted(() => ({
  getById: vi.fn(),
  updateAccount: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  categoriesApi: { list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() }
}))

vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById, update: updateAccount } } }))
vi.mock('@/api/admin/groupCategories', () => ({ default: categoriesApi }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

const group = (id: number, extra: Record<string, unknown> = {}) => ({ id, name: `Group ${id}`, platform: 'anthropic', status: 'active', ...extra }) as unknown as AdminGroup
const activeGroups = [group(1), group(2), group(3)]
const listRow = { id: 7, name: 'acc-7', platform: 'anthropic', group_ids: [3, 1] } as unknown as AccountListItem
const freshAccount = (overrides: Record<string, unknown> = {}) => ({
  id: 7,
  name: 'acc-7',
  platform: 'anthropic',
  group_ids: [3, 1],
  groups: [group(3), group(1)],
  extra: {},
  ...overrides
})

const BaseDialogStub = defineComponent({
  props: ['show', 'title'],
  emits: ['close'],
  template: '<div v-if="show" data-test="dialog"><h3>{{ title }}</h3><button data-test="dialog-x" @click="$emit(\'close\')">x</button><slot /><slot name="footer" /></div>'
})
const ConfirmDialogStub = defineComponent({
  props: ['show', 'message'],
  emits: ['confirm', 'cancel'],
  template: '<div v-if="show" data-test="mixed-confirm">{{ message }}<button data-test="mixed-yes" @click="$emit(\'confirm\')">yes</button><button data-test="mixed-no" @click="$emit(\'cancel\')">no</button></div>'
})
const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: { modelValue: Array, groups: Array, platform: String, mixedScheduling: Boolean, categoryTabs: Boolean, listClass: String },
  emits: ['update:modelValue'],
  template: '<div data-test="selector">{{ groups.map((g) => g.id).join(",") }}|{{ modelValue.join(",") }}</div>'
})

function mountDialog(account: AccountListItem = listRow) {
  return mount(AccountGroupsDialog, {
    props: { account, groups: activeGroups },
    global: { stubs: { BaseDialog: BaseDialogStub, ConfirmDialog: ConfirmDialogStub, GroupSelector: GroupSelectorStub } }
  })
}

const selector = (wrapper: ReturnType<typeof mountDialog>) => wrapper.getComponent(GroupSelectorStub)
const confirm = (wrapper: ReturnType<typeof mountDialog>) => wrapper.get('[data-test="account-groups-confirm"]').trigger('click')

beforeEach(() => {
  for (const fn of [getById, updateAccount, showError, showSuccess, categoriesApi.list]) fn.mockReset()
  categoriesApi.list.mockResolvedValue([])
  getById.mockResolvedValue(freshAccount())
})

describe('AccountGroupsDialog', () => {
  it('fetches the account on open and passes a tall picker with category tabs', async () => {
    const wrapper = mountDialog()
    expect(wrapper.text()).toContain('common.loading')
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(7)
    expect(wrapper.get('h3').text()).toBe('admin.groupCategories.editAccountGroups:{"name":"acc-7"}')
    const props = selector(wrapper).props()
    expect(props.modelValue).toEqual([3, 1])
    expect(props.categoryTabs).toBe(true)
    expect(props.listClass).toBe('!max-h-80')
    expect(props.platform).toBe('anthropic')
  })

  it('ignores a late response for an account that is no longer shown', async () => {
    let resolveFirst!: (value: unknown) => void
    getById
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValueOnce(freshAccount({ id: 8, name: 'acc-8', group_ids: [2] }))
    const wrapper = mountDialog()
    await wrapper.setProps({ account: { ...listRow, id: 8, name: 'acc-8' } })
    await flushPromises()
    resolveFirst(freshAccount())
    await flushPromises()

    expect(selector(wrapper).props('modelValue')).toEqual([2])
  })

  it('lists and ticks a bound inactive group', async () => {
    getById.mockResolvedValueOnce(freshAccount({ group_ids: [3, 9], groups: [group(3), group(9, { status: 'inactive' })] }))
    const wrapper = mountDialog()
    await flushPromises()

    expect(selector(wrapper).props('groups').map((g: AdminGroup) => g.id)).toEqual([1, 2, 3, 9])
    expect(selector(wrapper).props('modelValue')).toEqual([3, 9])
  })

  it('sends nothing on cancel or on an unchanged confirm', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('[data-test="dialog-x"]').trigger('click')
    await confirm(wrapper)

    expect(updateAccount).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toHaveLength(2)
  })

  it('appends a new tick and sends only group_ids', async () => {
    const updated = freshAccount({ group_ids: [3, 1, 2] })
    updateAccount.mockResolvedValueOnce(updated)
    const wrapper = mountDialog()
    await flushPromises()
    selector(wrapper).vm.$emit('update:modelValue', [3, 1, 2])
    await confirm(wrapper)
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledTimes(1)
    expect(updateAccount).toHaveBeenCalledWith(7, { group_ids: [3, 1, 2] })
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.accountUpdated')
    expect(wrapper.emitted('updated')).toEqual([[updated]])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('asks before resending with the mixed-channel flag, and keeps the dialog when the confirm is cancelled', async () => {
    updateAccount
      .mockRejectedValueOnce({ status: 409, error: 'mixed_channel_warning', message: 'mixed risk' })
      .mockRejectedValueOnce({ status: 409, error: 'mixed_channel_warning', message: 'mixed risk' })
      .mockResolvedValueOnce(freshAccount({ group_ids: [3] }))
    const wrapper = mountDialog()
    await flushPromises()
    selector(wrapper).vm.$emit('update:modelValue', [3])
    await confirm(wrapper)
    await flushPromises()

    expect(wrapper.get('[data-test="mixed-confirm"]').text()).toContain('mixed risk')
    await wrapper.get('[data-test="mixed-no"]').trigger('click')
    expect(wrapper.find('[data-test="mixed-confirm"]').exists()).toBe(false)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(selector(wrapper).props('modelValue')).toEqual([3])

    await confirm(wrapper)
    await flushPromises()
    await wrapper.get('[data-test="mixed-yes"]').trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenNthCalledWith(1, 7, { group_ids: [3] })
    expect(updateAccount).toHaveBeenNthCalledWith(3, 7, { group_ids: [3], confirm_mixed_channel_risk: true })
    expect(wrapper.emitted('updated')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(showError).not.toHaveBeenCalled()
  })

  it('keeps the dialog open on other errors', async () => {
    updateAccount.mockRejectedValueOnce({ status: 400, message: 'group not found' })
    const wrapper = mountDialog()
    await flushPromises()
    selector(wrapper).vm.$emit('update:modelValue', [1])
    await confirm(wrapper)
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('group not found')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.find('[data-test="selector"]').exists()).toBe(true)
  })

  it('closes with an error when the account cannot be loaded', async () => {
    getById.mockReset()
    getById.mockRejectedValueOnce({ message: 'not found' })
    const wrapper = mountDialog()
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('not found')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
