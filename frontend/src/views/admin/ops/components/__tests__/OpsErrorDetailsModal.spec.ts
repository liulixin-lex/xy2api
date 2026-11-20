import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailsModal from '../OpsErrorDetailsModal.vue'

const mocks = vi.hoisted(() => ({
  listRequestErrors: vi.fn(),
  listUpstreamErrors: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    listRequestErrors: mocks.listRequestErrors,
    listUpstreamErrors: mocks.listUpstreamErrors
  }
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('OpsErrorDetailsModal', () => {
  beforeEach(() => {
    mocks.listRequestErrors.mockReset()
    mocks.listUpstreamErrors.mockReset()
    mocks.listRequestErrors.mockResolvedValue({ items: [], total: 0 })
    mocks.listUpstreamErrors.mockResolvedValue({ items: [], total: 0 })
  })

  // The ops dashboard's deep link (?open_error_details=1) opens the list before the dialog mounts.
  it('loads its list when it mounts already open', async () => {
    shallowMount(OpsErrorDetailsModal, {
      props: { show: true, timeRange: '1h', errorType: 'upstream' }
    })
    await flushPromises()

    expect(mocks.listUpstreamErrors).toHaveBeenCalledTimes(1)
    expect(mocks.listUpstreamErrors.mock.calls[0][0]).toMatchObject({ page: 1, page_size: 10, phase: 'upstream' })
  })

  it('loads nothing while closed, and loads on opening', async () => {
    const wrapper = shallowMount(OpsErrorDetailsModal, {
      props: { show: false, timeRange: '1h', errorType: 'request' }
    })
    await flushPromises()
    expect(mocks.listRequestErrors).not.toHaveBeenCalled()

    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(mocks.listRequestErrors).toHaveBeenCalledTimes(1)
  })
})
