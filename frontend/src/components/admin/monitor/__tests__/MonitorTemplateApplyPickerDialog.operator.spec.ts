import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import type { AssociatedMonitorBrief } from '@/api/admin/channelMonitorTemplate'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { MeAccess } from '@/types/adminAccess'
import MonitorTemplateApplyPickerDialog from '../MonitorTemplateApplyPickerDialog.vue'

const mocks = vi.hoisted(() => ({ listAssociatedMonitors: vi.fn(), apply: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { channelMonitorTemplate: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

function seedOperator() {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { channels: 'manage' },
    flags: [],
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

function mountDialog(templateHasHeaders: boolean) {
  return mount(MonitorTemplateApplyPickerDialog, {
    props: { show: true, templateId: 3, templateName: 'Template', templateHasHeaders },
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } } }
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.listAssociatedMonitors.mockResolvedValue({
    items: [{ id: 11, name: 'Monitor 11', provider: 'openai', enabled: true } as AssociatedMonitorBrief]
  })
  mocks.apply.mockResolvedValue({ affected: 1 })
  seedOperator()
})

describe('MonitorTemplateApplyPickerDialog for operators', () => {
  it('disables apply for operators on templates with headers', async () => {
    const withHeaders = mountDialog(true)
    await flushPromises()
    const confirm = withHeaders.get('[data-testid="template-apply-confirm"]')
    expect(confirm.attributes('disabled')).toBeDefined()
    expect(withHeaders.get('[data-testid="template-apply-super-only"]').text()).toBe('admin.operatorAccess.templateApplySuperOnly')
    await confirm.trigger('click')
    await flushPromises()
    expect(mocks.apply).not.toHaveBeenCalled()
    withHeaders.unmount()

    const withoutHeaders = mountDialog(false)
    await flushPromises()
    expect(withoutHeaders.find('[data-testid="template-apply-super-only"]').exists()).toBe(false)
    const applicable = withoutHeaders.get('[data-testid="template-apply-confirm"]')
    expect(applicable.attributes('disabled')).toBeUndefined()
    await applicable.trigger('click')
    await flushPromises()
    expect(mocks.apply).toHaveBeenCalledWith(3, [11])
  })
})
