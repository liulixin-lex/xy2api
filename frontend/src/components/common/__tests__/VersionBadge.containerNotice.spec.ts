import { describe, it, expect, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'

const performUpdate = vi.fn()

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isAdmin: true }),
  useAppStore: () => appStore
}))
vi.mock('@/api/admin/system', () => ({
  performUpdate: () => performUpdate(),
  restartService: vi.fn(),
  getRollbackVersions: vi.fn(),
  rollback: vi.fn()
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))

const appStore = reactive({
  hasUpdate: true,
  releaseInfo: { name: 'v0.4.3', body: '', html_url: '#', published_at: '' },
  buildType: 'release',
  versionLoading: false,
  currentVersion: '0.4.2',
  latestVersion: '0.4.3',
  fetchVersion: vi.fn(),
  clearVersionCache: vi.fn()
})

async function updateWith(result: Record<string, unknown>) {
  performUpdate.mockResolvedValue(result)
  const { default: VersionBadge } = await import('../VersionBadge.vue')
  const wrapper = mount(VersionBadge, { global: { stubs: { Icon: true, transition: false } } })
  await wrapper.find('button').trigger('click')
  const update = wrapper.findAll('button').find((b) => b.text().includes('version.updateNow'))
  expect(update).toBeTruthy()
  await update!.trigger('click')
  await flushPromises()
  return wrapper
}

describe('VersionBadge container notice', () => {
  beforeEach(() => performUpdate.mockReset())

  it('tells a container deployment to update the image', async () => {
    const wrapper = await updateWith({ message: 'ok', need_restart: true, container: true })
    expect(wrapper.text()).toContain('version.restartRequired')
    expect(wrapper.find('[data-test="container-update-notice"]').text()).toBe('version.containerNotice')
  })

  it('shows no notice outside a container', async () => {
    const wrapper = await updateWith({ message: 'ok', need_restart: true })
    expect(wrapper.text()).toContain('version.restartRequired')
    expect(wrapper.find('[data-test="container-update-notice"]').exists()).toBe(false)
  })
})
