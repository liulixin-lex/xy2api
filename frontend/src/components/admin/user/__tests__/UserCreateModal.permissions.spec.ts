import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import UserCreateModal from '../UserCreateModal.vue'

const mocks = vi.hoisted(() => ({
  create: vi.fn(), permissions: vi.fn(), success: vi.fn(), error: vi.fn(),
  auth: { isAdmin: true, user: { id: 1, role: 'admin' } }
}))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { create: mocks.create, updateOperatorPermissions: mocks.permissions } } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success, showError: mocks.error }) }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await import('vue')
  return { useAdminPermission: () => ({ isSuperAdmin: computed(() => mocks.auth.isAdmin), has: () => mocks.auth.isAdmin }) }
})
const globals = { stubs: {
  BaseDialog: { props: ['show', 'width'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  Icon: true, TotpStepUpDialog: true
} }
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.clearAllMocks()
  mocks.auth = reactive({ isAdmin: true, user: { id: 1, role: 'admin' } })
  mocks.create.mockResolvedValue({ id: 88, role: 'operator' })
  mocks.permissions.mockResolvedValue({ modules: { support_tickets: 'manage' }, flags: [] })
})
const open = () => mount(UserCreateModal, { props: { show: true }, global: globals })
async function fill(wrapper: ReturnType<typeof open>, role = 'operator') {
  await wrapper.get('input[type="email"]').setValue('new-operator@example.test')
  await wrapper.get('input[type="text"]').setValue('mock-password')
  await wrapper.get('select').setValue(role)
}

describe('new operator permissions', () => {
  it('defaults every permission to off and saves selected ticket handling after creating the operator', async () => {
    const wrapper = open()
    await fill(wrapper)
    expect((wrapper.get('[data-test="operator-view-support_tickets"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-test="operator-manage-support_tickets"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-test="operator-manage-support_tickets"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledOnce()
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ role: 'operator', email: 'new-operator@example.test' }))
    expect(mocks.permissions).toHaveBeenCalledWith(88, { modules: { support_tickets: 'manage' }, flags: [] })
    expect(mocks.create.mock.invocationCallOrder[0]).toBeLessThan(mocks.permissions.mock.invocationCallOrder[0])
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('retains the created user ID on partial failure, then retries permissions without creating another account', async () => {
    mocks.permissions.mockRejectedValueOnce({ message: 'mock permission save failure' })
    const wrapper = open()
    await fill(wrapper)
    await wrapper.get('[data-test="operator-view-support_tickets"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-test="operator-created"]').text()).toContain('createdPermissionsPending')
    expect(wrapper.get('fieldset').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledTimes(1)
    expect(mocks.permissions).toHaveBeenCalledTimes(2)
    expect(mocks.permissions).toHaveBeenLastCalledWith(88, { modules: { support_tickets: 'view' }, flags: [] })
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('retains no permissions from a dismissed create form', async () => {
    const wrapper = open()
    await fill(wrapper)
    await wrapper.get('[data-test="operator-manage-support_tickets"]').setValue(true)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await fill(wrapper)
    expect((wrapper.get('[data-test="operator-view-support_tickets"]').element as HTMLInputElement).checked).toBe(false)
  })

  it('does not assign permissions to regular users', async () => {
    const wrapper = open()
    await fill(wrapper, 'user')
    expect(wrapper.find('[data-test="operator-permissions-table"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.permissions).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('does not grant permissions after the form closes while creation is in flight', async () => {
    let resolve!: (value: { id: number }) => void
    mocks.create.mockReturnValueOnce(new Promise<{ id: number }>(ok => { resolve = ok }))
    const wrapper = open()
    await fill(wrapper)
    await wrapper.get('[data-test="operator-manage-support_tickets"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    await wrapper.setProps({ show: false })
    resolve({ id: 88 })
    await flushPromises()
    expect(mocks.permissions).not.toHaveBeenCalled()
    expect(mocks.success).not.toHaveBeenCalled()
  })
})
