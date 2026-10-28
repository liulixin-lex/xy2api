import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { AdminFlagId, MeAccess } from '@/types/adminAccess'
import ProxiesView from '../ProxiesView.vue'

const { list, update, getAllWithCount } = vi.hoisted(() => ({ list: vi.fn(), update: vi.fn(), getAllWithCount: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, update, getAllWithCount } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const NETWORK_FIELDS = ['protocol', 'host', 'port', 'username', 'password', 'fallback_mode', 'backup_proxy_id']

function seedOperator(flags: AdminFlagId[]) {
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { proxies: 'manage', accounts: 'view' },
    flags,
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
}

const mountView = () => shallowMount(ProxiesView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
    DataTable: { props: ['data'], template: '<div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  } },
})

let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({
    items: [{ id: 9, name: 'proxy', protocol: 'http', host: 'proxy.example', port: 8080, username: 'old-user', status: 'active', fallback_mode: 'none' }],
    total: 1,
    pages: 1
  })
  getAllWithCount.mockResolvedValue([])
  update.mockResolvedValue({})
})
afterEach(() => wrapper?.unmount())

async function openEdit() {
  wrapper = mountView()
  await flushPromises()
  await wrapper.findAll('button').find(button => button.text() === 'common.edit')!.trigger('click')
}

async function submitEdit() {
  await wrapper.get('#edit-proxy-form').trigger('submit')
  await flushPromises()
  expect(update).toHaveBeenCalledTimes(1)
  return update.mock.calls[0][1] as Record<string, unknown>
}

describe('ProxiesView for operators', () => {
  it('hides proxy create and keeps network fields read-only without the upstream-address flag', async () => {
    seedOperator([])
    await openEdit()

    expect(wrapper.find('[data-testid="proxy-create"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.proxies.dataImport')
    expect(wrapper.get('[data-testid="proxy-network-needs-flag"]').text()).toBe('admin.operatorAccess.proxyNetworkNeedsFlag')
    expect(wrapper.get('[data-testid="proxy-network-fields"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="proxy-fallback-fields"]').attributes('disabled')).toBeDefined()

    await wrapper.findAll<HTMLInputElement>('#edit-proxy-form input').find(input => input.element.value === 'proxy')!.setValue('renamed')
    const payload = await submitEdit()
    expect(payload.name).toBe('renamed')
    for (const field of NETWORK_FIELDS) expect(payload).not.toHaveProperty(field)
  })

  it('lets a flag holder create proxies and edit network fields', async () => {
    seedOperator(['accounts.upstream_address'])
    await openEdit()

    expect(wrapper.find('[data-testid="proxy-create"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="proxy-network-needs-flag"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="proxy-network-fields"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="proxy-fallback-fields"]').attributes('disabled')).toBeUndefined()

    await wrapper.findAll<HTMLInputElement>('#edit-proxy-form input').find(input => input.element.value === 'proxy.example')!.setValue('203.0.113.10')
    const payload = await submitEdit()
    expect(payload).toMatchObject({ host: '203.0.113.10', port: 8080, protocol: 'http', username: 'old-user', fallback_mode: 'none' })
  })

  it('sends status and expiry only when the operator changed them', async () => {
    seedOperator([])
    list.mockResolvedValue({
      items: [{ id: 9, name: 'proxy', protocol: 'http', host: 'proxy.example', port: 8080, status: 'expired', expires_at: '2026-01-01T00:00:00Z', fallback_mode: 'none' }],
      total: 1,
      pages: 1
    })
    await openEdit()
    await wrapper.findAll<HTMLInputElement>('#edit-proxy-form input').find(input => input.element.value === 'proxy')!.setValue('renamed')
    const payload = await submitEdit()
    expect(payload.name).toBe('renamed')
    expect(payload).not.toHaveProperty('status')
    expect(payload).not.toHaveProperty('expires_at')

    update.mockClear()
    wrapper.unmount()
    await openEdit()
    await wrapper.get<HTMLInputElement>('#edit-proxy-form input[type="date"]').setValue('2027-01-01')
    const extended = await submitEdit()
    expect(extended.expires_at).toBe(Math.floor(new Date('2027-01-01').getTime() / 1000))
    expect(extended).not.toHaveProperty('status')
  })
})
