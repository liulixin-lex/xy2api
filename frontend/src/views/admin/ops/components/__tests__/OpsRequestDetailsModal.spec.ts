import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import type { OpsDashboardOverview } from '@/api/admin/ops'
import { flushPromises, mount, shallowMount } from '@vue/test-utils'
import OpsDashboardHeader from '../OpsDashboardHeader.vue'
import OpsRequestDetailsModal from '../OpsRequestDetailsModal.vue'

const { listRequestDetails, viewport } = vi.hoisted(() => ({
  listRequestDetails: vi.fn(),
  viewport: { desktop: true },
}))

vi.mock('@vueuse/core', () => ({ useMediaQuery: () => ref(viewport.desktop) }))
vi.mock('@/api/admin/ops', () => ({ opsAPI: { listRequestDetails } }))
vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() }),
  useAdminSettingsStore: () => ({ opsRealtimeMonitoringEnabled: false }),
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

async function openDetails(sort: 'duration_desc' | 'e2e_first_output_desc') {
  const wrapper = mount(OpsRequestDetailsModal, {
    props: { modelValue: false, timeRange: '1h', preset: { title: 'Details', sort } },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Pagination: true } },
  })
  await wrapper.setProps({ modelValue: true })
  await flushPromises()
  return wrapper
}

describe('Ops request latency details', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    viewport.desktop = true
    listRequestDetails.mockResolvedValue({
      items: [
        { kind: 'success', created_at: '2026-09-10T00:00:00Z', duration_ms: 12000, first_token_ms: 800, end_to_end_first_output_ms: 51000 },
        { kind: 'success', created_at: '2026-09-10T00:00:01Z', duration_ms: 9000, first_token_ms: 300, end_to_end_first_output_ms: 0 },
        { kind: 'success', created_at: '2026-09-10T00:00:02Z', duration_ms: 5000, first_token_ms: 200, end_to_end_first_output_ms: null },
      ],
      total: 3,
    })
  })

  it('opens the user-wait card with end-to-end sorting and successful requests', async () => {
    const wrapper = shallowMount(OpsDashboardHeader, {
      props: { overview: {} as OpsDashboardOverview, platform: '', groupId: null, timeRange: '1h', queryMode: 'auto', loading: false, lastUpdated: null },
    })
    await flushPromises()
    const button = wrapper.get('[data-testid="ops-e2e-card"]').findAll('button').find((item) => item.text() === 'admin.ops.requestDetails.details')
    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('openRequestDetails')).toEqual([[
      { title: 'admin.ops.e2eFirstOutput.detailsTitle', kind: 'success', sort: 'e2e_first_output_desc' },
    ]])
    wrapper.unmount()
  })

  it.each([true, false])('shows the end-to-end first output rather than the last attempt or total duration (desktop: %s)', async (desktop) => {
    viewport.desktop = desktop
    const wrapper = await openDetails('e2e_first_output_desc')
    expect(listRequestDetails).toHaveBeenCalledWith(expect.objectContaining({ sort: 'e2e_first_output_desc' }))
    expect(wrapper.text()).toContain('admin.ops.e2eFirstOutput.detailsTitle')
    expect(wrapper.text()).toContain('51000 ms')
    expect(wrapper.text()).toContain('0 ms')
    for (const other of ['800 ms', '300 ms', '200 ms', '12000 ms', '9000 ms', '5000 ms']) expect(wrapper.text()).not.toContain(other)
    // No end-to-end record: unknown, not the last attempt's first token.
    if (desktop) expect(wrapper.findAll('tbody tr')[2].findAll('td')[4].text()).toBe('admin.ops.e2eFirstOutput.unknown')
    else expect(wrapper.text()).toContain('admin.ops.e2eFirstOutput.detailsTitle: admin.ops.e2eFirstOutput.unknown')
    wrapper.unmount()
  })

  it('keeps total duration for duration details', async () => {
    const wrapper = await openDetails('duration_desc')
    expect(listRequestDetails).toHaveBeenCalledWith(expect.objectContaining({ sort: 'duration_desc' }))
    expect(wrapper.text()).toContain('admin.ops.requestDetails.table.duration')
    expect(wrapper.text()).toContain('12000 ms')
    expect(wrapper.text()).not.toContain('800 ms')
    wrapper.unmount()
  })
})
