import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

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
    global: {
      plugins: [getActivePinia()!],
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        Select: true,
        Icon: true,
        UserAttributeForm: true,
        TotpStepUpDialog: true
      }
    }
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
