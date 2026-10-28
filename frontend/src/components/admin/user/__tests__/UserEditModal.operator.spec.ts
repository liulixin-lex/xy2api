import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { getActivePinia } from 'pinia'
import type { AdminUser } from '@/types'

import UserEditModal from '../UserEditModal.vue'

const {
  update,
  getOperatorPermissions,
  updateOperatorPermissions,
  updateUserAttributeValues,
  showSuccess,
  showError,
  stepUp
} = vi.hoisted(() => {
  const state = { depth: 0 }
  return {
    update: vi.fn(),
    getOperatorPermissions: vi.fn(),
    updateOperatorPermissions: vi.fn(),
    updateUserAttributeValues: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn(),
    stepUp: {
      state,
      run: vi.fn(async (action: () => Promise<unknown>) => {
        state.depth += 1
        try {
          return await action()
        } finally {
          state.depth -= 1
        }
      })
    }
  }
})

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { update, getOperatorPermissions, updateOperatorPermissions },
    userAttributes: { updateUserAttributeValues }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('@/composables/useStepUp', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/composables/useStepUp')>()),
  useStepUp: () => ({ run: stepUp.run })
}))

// TotpStepUpDialog pulls in the API client, which needs the real i18n instance.
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${JSON.stringify(params)}` : key
  })
}))

type Role = 'admin' | 'operator' | 'user'

const member = (id = 7, role: Role = 'operator') => ({
  id, email: `member-${id}@example.test`, username: 'member', notes: '', role, concurrency: 1, rpm_limit: 0
}) as AdminUser

const modalGlobals = () => ({
  plugins: [getActivePinia()!],
  stubs: {
    BaseDialog: {
      name: 'BaseDialog',
      props: ['show', 'title', 'width'],
      template: '<div v-if="show"><slot /><slot name="footer" /></div>'
    },
    Select: true,
    Icon: true,
    UserAttributeForm: true,
    TotpStepUpDialog: true
  }
})

enableAutoUnmount(afterEach)

function mountModal(signedInRole: Role, editedRole: Role) {
  getActivePinia()!.state.value.auth = { user: { id: 1, role: signedInRole }, runMode: 'standard' }
  return mount(UserEditModal, {
    props: {
      show: true,
      user: {
        id: 7,
        email: 'member@example.test',
        username: 'member',
        notes: '',
        role: editedRole,
        concurrency: 1,
        rpm_limit: 0
      } as never
    },
    global: modalGlobals()
  })
}

describe('UserEditModal operator role', () => {
  beforeEach(() => {
    for (const mock of [update, getOperatorPermissions, updateOperatorPermissions, updateUserAttributeValues, showSuccess, showError]) {
      mock.mockReset()
    }
    stepUp.run.mockClear()
    stepUp.state.depth = 0
    update.mockResolvedValue({})
    updateOperatorPermissions.mockResolvedValue({ modules: {}, flags: [] })
    getOperatorPermissions.mockResolvedValue({ modules: { users: 'view' }, flags: [] })
  })

  it.each([
    ['admin', 'operator', 'extra-wide'],
    ['admin', 'user', 'normal'],
    ['admin', 'admin', 'normal'],
    ['operator', 'operator', 'normal'],
    ['operator', 'user', 'normal']
  ] as const)('uses %s editing %s dialog width %s', async (signedInRole, editedRole, width) => {
    const wrapper = mountModal(signedInRole, editedRole)
    await flushPromises()

    expect(wrapper.findComponent({ name: 'BaseDialog' }).props('width')).toBe(width)
    const form = wrapper.get('#edit-user-form')
    expect(form.classes().includes('grid')).toBe(width === 'extra-wide')
    expect(wrapper.find('[data-test="operator-permissions-panel"]').exists()).toBe(width === 'extra-wide')
    if (width === 'normal') expect(form.classes()).toContain('space-y-5')
  })

  it('keeps user fields together before permissions and switches to columns only at lg', async () => {
    const wrapper = mountModal('admin', 'operator')
    await flushPromises()

    const form = wrapper.get('#edit-user-form')
    expect(form.classes()).toEqual(expect.arrayContaining(['grid-cols-1', 'lg:grid-cols-2']))
    expect(Array.from(form.element.children).map(child => child.getAttribute('data-test'))).toEqual([
      'user-edit-fields', 'operator-permissions-panel'
    ])
    const fields = wrapper.get('[data-test="user-edit-fields"]')
    expect(fields.classes()).toContain('space-y-5')
    expect(fields.findAll('label.input-label').map(label => label.text())).toEqual([
      'admin.users.email',
      'admin.users.password',
      'admin.users.username',
      'admin.users.form.roleLabel',
      'admin.users.notes',
      'admin.users.columns.concurrency',
      'admin.users.form.rpmLimit'
    ])
    expect(fields.findComponent({ name: 'UserAttributeForm' }).exists()).toBe(true)
    expect(fields.find('[data-test="operator-permissions-table"]').exists()).toBe(false)
  })

  it('widens and restores the layout when the selected role changes', async () => {
    const wrapper = mountModal('admin', 'user')
    const dialog = wrapper.findComponent({ name: 'BaseDialog' })
    const roleSelect = wrapper.findComponent({ name: 'Select' })

    roleSelect.vm.$emit('update:modelValue', 'operator')
    await flushPromises()
    expect(dialog.props('width')).toBe('extra-wide')
    expect(wrapper.find('[data-test="operator-permissions-table"]').exists()).toBe(true)

    for (const role of ['user', 'admin']) {
      roleSelect.vm.$emit('update:modelValue', role)
      await flushPromises()
      expect(dialog.props('width')).toBe('normal')
      expect(wrapper.get('#edit-user-form').classes()).not.toContain('grid')
      expect(wrapper.find('[data-test="operator-permissions-panel"]').exists()).toBe(false)
    }
  })

  it('loads the table when the always-mounted UsersView modal receives user then show in one click', async () => {
    let resolvePermissions!: (value: unknown) => void
    getOperatorPermissions.mockImplementationOnce(() => new Promise(resolve => { resolvePermissions = resolve }))
    getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }
    // UsersView.handleEdit assigns editingUser, then showEditModal in the same tick;
    // UserEditModal stays mounted with null/false between openings.
    const wrapper = mount(defineComponent({
      components: { UserEditModal },
      setup() {
        const editingUser = ref<AdminUser | null>(null)
        const showEditModal = ref(false)
        const open = () => { editingUser.value = member(); showEditModal.value = true }
        const close = () => { showEditModal.value = false; editingUser.value = null }
        return { editingUser, showEditModal, open, close }
      },
      // The browser applies the user prop before the Boolean-cast show prop.
      render() {
        return [h('button', { onClick: this.open }, 'edit'), h(UserEditModal, {
          user: this.editingUser, show: this.showEditModal, onClose: this.close
        })]
      }
    }), { global: modalGlobals() })

    await wrapper.get('button').trigger('click')
    await flushPromises()
    resolvePermissions({ modules: { users: 'view' }, flags: [] })
    await flushPromises()

    expect(getOperatorPermissions).toHaveBeenCalledTimes(1)
    expect(getOperatorPermissions).toHaveBeenCalledWith(7)
    expect(wrapper.find('[data-test="operator-permissions-table"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="operator-view-users"]').element).toHaveProperty('checked', true)
  })

  it.each(['user-first', 'show-first'] as const)('loads with separate prop updates (%s)', async (order) => {
    const wrapper = mountModal('admin', 'user')
    await wrapper.setProps({ show: false, user: null })
    if (order === 'user-first') {
      await wrapper.setProps({ user: member() })
      expect(getOperatorPermissions).not.toHaveBeenCalled()
      await wrapper.setProps({ show: true })
    } else {
      await wrapper.setProps({ show: true })
      expect(getOperatorPermissions).not.toHaveBeenCalled()
      await wrapper.setProps({ user: member() })
    }
    await flushPromises()
    expect(wrapper.find('[data-test="operator-permissions-table"]').exists()).toBe(true)
    expect(getOperatorPermissions).toHaveBeenCalledTimes(1)
    expect(getOperatorPermissions).toHaveBeenCalledWith(7)
  })

  it('reloads stored permissions when reopening the same user after unsaved edits', async () => {
    const wrapper = mountModal('admin', 'operator')
    await flushPromises()
    await wrapper.get('[data-test="operator-manage-users"]').setValue(true)
    await wrapper.setProps({ show: false })
    getOperatorPermissions.mockResolvedValue({ modules: { accounts: 'view' }, flags: [] })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(getOperatorPermissions).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="operator-manage-users"]').element).toHaveProperty('checked', false)
    expect(wrapper.get('[data-test="operator-view-accounts"]').element).toHaveProperty('checked', true)
  })

  it('loads when the role select changes to operator', async () => {
    const wrapper = mountModal('admin', 'user')
    await flushPromises()
    expect(getOperatorPermissions).not.toHaveBeenCalled()
    wrapper.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'operator')
    await flushPromises()
    expect(getOperatorPermissions).toHaveBeenCalledTimes(1)
    expect(getOperatorPermissions).toHaveBeenCalledWith(7)
    expect(wrapper.find('[data-test="operator-permissions-table"]').exists()).toBe(true)
  })

  it.each(['switch-user', 'reopen-same-user'] as const)('ignores a stale response after %s', async (action) => {
    let resolveOld!: (value: unknown) => void
    getOperatorPermissions.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountModal('admin', 'operator')
    await flushPromises()
    if (action === 'switch-user') await wrapper.setProps({ user: member(8) })
    else {
      await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true })
    }
    await flushPromises()
    expect(getOperatorPermissions).toHaveBeenCalledTimes(2)
    resolveOld({ modules: { accounts: 'manage' }, flags: ['data.export'] })
    await flushPromises()

    expect(wrapper.get('[data-test="operator-view-users"]').element).toHaveProperty('checked', true)
    expect(wrapper.get('[data-test="operator-manage-accounts"]').element).toHaveProperty('checked', false)
    expect(wrapper.get('[data-test="operator-flag-data.export"]').element).toHaveProperty('checked', false)
  })

  it('saves operator permissions before the user, both through step-up', async () => {
    const calls: string[] = []
    updateOperatorPermissions.mockImplementation(async () => {
      calls.push(`permissions:${stepUp.state.depth}`)
      return { modules: {}, flags: [] }
    })
    update.mockImplementation(async () => {
      calls.push(`user:${stepUp.state.depth}`)
      return {}
    })

    const wrapper = mountModal('admin', 'operator')
    await flushPromises()

    expect(getOperatorPermissions).toHaveBeenCalledWith(7)
    await wrapper.get('[data-test="operator-manage-users"]').setValue(true)
    await wrapper.get('[data-test="operator-flag-data.export"]').setValue(true)

    await wrapper.get('form#edit-user-form').trigger('submit')
    await flushPromises()

    // Each save ran inside its own step-up run, permissions first.
    expect(calls).toEqual(['permissions:1', 'user:1'])
    expect(stepUp.run).toHaveBeenCalledTimes(2)
    expect(updateOperatorPermissions).toHaveBeenCalledWith(7, {
      modules: { users: 'manage' },
      flags: ['data.export']
    })
    expect(update).toHaveBeenCalledWith(7, expect.objectContaining({ role: 'operator', email: 'member@example.test' }))
    expect(showSuccess).toHaveBeenCalledWith('admin.users.userUpdated')
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('does not save the user when saving operator permissions fails', async () => {
    updateOperatorPermissions.mockRejectedValue(new Error('denied'))

    const wrapper = mountModal('admin', 'operator')
    await flushPromises()
    await wrapper.get('form#edit-user-form').trigger('submit')
    await flushPromises()

    expect(updateOperatorPermissions).toHaveBeenCalledTimes(1)
    expect(update).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('denied')
  })

  it('omits role from the payload when an operator edits a user', async () => {
    const wrapper = mountModal('operator', 'operator')
    await flushPromises()

    // Operators see neither the role select nor the permissions table.
    expect(wrapper.find('[data-test="operator-permissions-table"]').exists()).toBe(false)
    expect(getOperatorPermissions).not.toHaveBeenCalled()

    await wrapper.get('form#edit-user-form').trigger('submit')
    await flushPromises()

    expect(updateOperatorPermissions).not.toHaveBeenCalled()
    expect(update).toHaveBeenCalledTimes(1)
    const payload = update.mock.calls[0][1] as Record<string, unknown>
    expect(payload).not.toHaveProperty('role')
    expect(payload).toMatchObject({ email: 'member@example.test', username: 'member', concurrency: 1, rpm_limit: 0 })
    expect(stepUp.run).toHaveBeenCalledTimes(1)
  })

  it('shows the reserved email message for field email', async () => {
    update.mockRejectedValue({ reason: 'ADMIN_FIELD_FORBIDDEN', metadata: { field: 'email' }, message: 'forbidden' })

    const wrapper = mountModal('operator', 'user')
    await flushPromises()
    expect(wrapper.find('[data-test="email-error"]').exists()).toBe(false)

    await wrapper.get('form#edit-user-form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-test="email-error"]').text()).toBe('admin.operatorAccess.errors.reservedEmail')
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toBeUndefined()

    // Another forbidden field is not reported as the email.
    update.mockRejectedValue({ reason: 'ADMIN_FIELD_FORBIDDEN', metadata: { field: 'role' }, message: 'role forbidden' })
    await wrapper.get('form#edit-user-form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[data-test="email-error"]').exists()).toBe(false)
    expect(showError).toHaveBeenCalledWith('role forbidden')
  })
})
