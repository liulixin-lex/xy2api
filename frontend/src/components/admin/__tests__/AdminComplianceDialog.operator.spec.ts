import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { reactive } from 'vue'

const stores = vi.hoisted(() => ({
  auth: null as null | Record<string, unknown>,
  compliance: null as null | Record<string, unknown>
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh'
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => stores.auth,
  useAdminComplianceStore: () => stores.compliance,
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() })
}))

import AdminComplianceDialog from '../AdminComplianceDialog.vue'

function mountDialog(role: 'admin' | 'operator' | 'user', shouldShow: boolean) {
  stores.auth = reactive({
    isAuthenticated: true,
    isAdmin: role === 'admin',
    canAccessAdmin: role === 'admin' || role === 'operator',
    logout: vi.fn()
  })
  stores.compliance = reactive({
    shouldShow,
    submitting: false,
    expectedPhrase: '我已阅读并同意',
    status: { required: shouldShow, version: 'v-test' },
    accept: vi.fn()
  })
  return mount(AdminComplianceDialog, {
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show" data-test="compliance-dialog">{{ title }}<slot /><slot name="footer" /></div>'
        },
        Input: true,
        Icon: true
      }
    }
  })
}

describe('AdminComplianceDialog for operators', () => {
  it('shows the compliance dialog to an operator', () => {
    const operator = mountDialog('operator', true)
    const dialog = operator.get('[data-test="compliance-dialog"]')
    expect(dialog.text()).toContain('adminCompliance.title')
    expect(dialog.text()).toContain('我已阅读并同意')

    // Nothing to acknowledge: no dialog.
    expect(mountDialog('operator', false).find('[data-test="compliance-dialog"]').exists()).toBe(false)
    // Plain users never see it; super admins still do.
    expect(mountDialog('user', true).find('[data-test="compliance-dialog"]').exists()).toBe(false)
    expect(mountDialog('admin', true).find('[data-test="compliance-dialog"]').exists()).toBe(true)
  })
})
