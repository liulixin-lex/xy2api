import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ErrorPassthroughRulesModal from '../ErrorPassthroughRulesModal.vue'
import TLSFingerprintProfilesModal from '../TLSFingerprintProfilesModal.vue'
import ScheduledTestsPanel from '../account/ScheduledTestsPanel.vue'

const api = vi.hoisted(() => ({
  listRules: vi.fn(),
  toggleRule: vi.fn(),
  listProfiles: vi.fn(),
  listPlans: vi.fn(),
  updatePlan: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    errorPassthrough: { list: api.listRules, toggleEnabled: api.toggleRule },
    tlsFingerprintProfiles: { list: api.listProfiles },
    scheduledTests: { listByAccount: api.listPlans, update: api.updatePlan }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const stubs = {
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  ConfirmDialog: true,
  Select: true,
  Icon: true
}

const stamp = { created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z' }

beforeEach(() => {
  vi.clearAllMocks()
  api.listRules.mockResolvedValue([{
    id: 1, name: 'rule', enabled: true, priority: 1, error_codes: [429], keywords: [], match_mode: 'any', platforms: [],
    passthrough_code: true, response_code: null, passthrough_body: true, custom_message: null, skip_monitoring: false,
    description: null, ...stamp
  }])
  api.listProfiles.mockResolvedValue([{
    id: 1, name: 'profile', description: null, enable_grease: false, cipher_suites: [], curves: [], point_formats: [],
    signature_algorithms: [], alpn_protocols: ['h2'], supported_versions: [], key_share_groups: [], psk_modes: [],
    extensions: [], ...stamp
  }])
  api.listPlans.mockResolvedValue([{
    id: 1, account_id: 5, model_id: 'gpt-test', cron_expression: '0 * * * *', enabled: true, max_results: 20,
    auto_recover: false, last_run_at: null, next_run_at: null, ...stamp
  }])
})

async function open<T>(component: T, props: Record<string, unknown>) {
  const wrapper = mount(component as never, { props: { show: false, ...props }, global: { stubs } })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

const titles = (wrapper: Awaited<ReturnType<typeof open>>) =>
  wrapper.findAll('button').map((button) => button.attributes('title') || button.text())

describe('account tool panels in read-only mode', () => {
  it('error passthrough rules: no create, edit, delete or enable toggle', async () => {
    const readonly = await open(ErrorPassthroughRulesModal, { readonly: true })
    expect(readonly.text()).toContain('rule')
    expect(titles(readonly)).not.toContain('common.edit')
    expect(titles(readonly)).not.toContain('common.delete')
    expect(readonly.text()).not.toContain('admin.errorPassthrough.createRule')
    const toggle = readonly.findAll('td button')[0]
    expect(toggle.attributes('disabled')).toBeDefined()
    await toggle.trigger('click')
    expect(api.toggleRule).not.toHaveBeenCalled()

    const editable = await open(ErrorPassthroughRulesModal, {})
    expect(editable.text()).toContain('admin.errorPassthrough.createRule')
    expect(titles(editable)).toEqual(expect.arrayContaining(['common.edit', 'common.delete']))
    expect(editable.findAll('td button')[0].attributes('disabled')).toBeUndefined()
  })

  it('TLS fingerprint profiles: no create, edit or delete', async () => {
    const readonly = await open(TLSFingerprintProfilesModal, { readonly: true })
    expect(readonly.text()).toContain('profile')
    expect(readonly.text()).not.toContain('admin.tlsFingerprintProfiles.createProfile')
    expect(titles(readonly)).not.toContain('common.edit')
    expect(titles(readonly)).not.toContain('common.delete')

    const editable = await open(TLSFingerprintProfilesModal, {})
    expect(editable.text()).toContain('admin.tlsFingerprintProfiles.createProfile')
    expect(titles(editable)).toEqual(expect.arrayContaining(['common.edit', 'common.delete']))
  })

  it('scheduled tests: no add, edit, delete or enable toggle', async () => {
    const readonly = await open(ScheduledTestsPanel, { readonly: true, accountId: 5, modelOptions: [] })
    expect(readonly.text()).toContain('gpt-test')
    expect(readonly.text()).not.toContain('admin.scheduledTests.addPlan')
    expect(titles(readonly)).not.toContain('admin.scheduledTests.editPlan')
    const toggle = readonly.get('[role="switch"]')
    expect(toggle.attributes('disabled')).toBeDefined()
    await toggle.trigger('click')
    expect(api.updatePlan).not.toHaveBeenCalled()

    const editable = await open(ScheduledTestsPanel, { accountId: 5, modelOptions: [] })
    expect(editable.text()).toContain('admin.scheduledTests.addPlan')
    expect(titles(editable)).toContain('admin.scheduledTests.editPlan')
  })
})
