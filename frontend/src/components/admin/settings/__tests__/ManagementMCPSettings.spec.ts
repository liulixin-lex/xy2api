import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import ManagementMCPSettings from '../ManagementMCPSettings.vue'
import zh from '@/i18n/locales/zh/managementMcp'
import en from '@/i18n/locales/en/managementMcp'

const mocks = vi.hoisted(() => ({
  get: vi.fn(), update: vi.fn(), refresh: vi.fn(), success: vi.fn(),
  stepUp: vi.fn((fn: () => unknown) => fn())
}))
vi.mock('@/api/admin/managementMcp', () => ({ getManagementMCPSettings: mocks.get, updateManagementMCPSettings: mocks.update }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ fetchPublicSettings: mocks.refresh, showSuccess: mocks.success }) }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: mocks.stepUp }), isStepUpCancelled: () => false }))
vi.mock('@/components/auth/TotpStepUpDialog.vue', () => ({ default: { template: '<div />' } }))

const render = () => mount(ManagementMCPSettings, {
  global: {
    plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh: { managementMcp: zh } } })],
    stubs: { RouterLink: { template: '<a><slot /></a>' } }
  }
})

beforeEach(() => {
  vi.clearAllMocks()
  mocks.get.mockResolvedValue({ enabled: false, public_url: '' })
  mocks.update.mockImplementation(async input => input)
})

describe('management MCP admin settings', () => {
  it('keeps translation keys aligned', () => {
    expect(Object.keys(zh).sort()).toEqual(Object.keys(en).sort())
  })

  it('does not allow an unknown configuration to be overwritten after loading fails', async () => {
    mocks.get.mockRejectedValueOnce(new Error('offline'))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('offline')
    expect(wrapper.get('[data-test="save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="save"]').trigger('click')
    expect(mocks.update).not.toHaveBeenCalled()
    await wrapper.findAll('button').find(button => !button.attributes('data-test'))!.trigger('click')
    await flushPromises()
    expect(mocks.get).toHaveBeenCalledTimes(2)
  })

  it('requires a URL when enabled and refreshes navigation after a verified save', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-test="enabled"]').setValue(true)
    expect(wrapper.get('[data-test="save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="public-url"]').setValue(' https://api.example/api/v1/mcp ')
    await wrapper.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(mocks.stepUp).toHaveBeenCalledOnce()
    expect(mocks.update).toHaveBeenCalledWith({ enabled: true, public_url: 'https://api.example/api/v1/mcp' })
    expect(mocks.refresh).toHaveBeenCalledWith(true)
    expect(wrapper.find('a').exists()).toBe(true)
  })

  it('retains the configured URL when disabling', async () => {
    mocks.get.mockResolvedValue({ enabled: true, public_url: 'https://api.example/api/v1/mcp' })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-test="enabled"]').setValue(false)
    await wrapper.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(mocks.update).toHaveBeenCalledWith({ enabled: false, public_url: 'https://api.example/api/v1/mcp' })
    // Disabling calls must still allow the administrator to revoke old grants.
    expect(wrapper.find('a').exists()).toBe(true)
  })

  it('preserves the draft after a save error', async () => {
    mocks.update.mockRejectedValueOnce(new Error('invalid URL'))
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-test="public-url"]').setValue('https://api.example/wrong')
    await wrapper.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('invalid URL')
    expect((wrapper.get('[data-test="public-url"]').element as HTMLInputElement).value).toBe('https://api.example/wrong')
    expect(mocks.success).not.toHaveBeenCalled()
    expect(mocks.refresh).not.toHaveBeenCalled()
  })
})
