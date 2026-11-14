import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { MeAccess } from '@/types/adminAccess'
import ProxiesView from '../ProxiesView.vue'

const { list, getAllWithCount, batchDelete } = vi.hoisted(() => ({
  list: vi.fn(),
  getAllWithCount: vi.fn(),
  batchDelete: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, getAllWithCount, batchDelete } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const mountView = () => shallowMount(ProxiesView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
    TableToolbar: false,
    DataTable: {
      props: ['data', 'selectedKeys'],
      emits: ['update:selectedKeys'],
      template: '<button data-test="pick" @click="$emit(\'update:selectedKeys\', data.map(row => row.id))">{{ selectedKeys.join(",") }}</button>'
    },
    ConfirmDialog: {
      props: ['show', 'title'],
      emits: ['confirm'],
      template: '<button v-if="show" :data-dialog="title" @click="$emit(\'confirm\')" />'
    }
  } }
})

let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  getActivePinia()!.state.value.auth = { user: { id: 7, role: 'operator' }, runMode: 'standard' }
  const access = useAdminAccessStore()
  access.access = {
    role: 'operator',
    is_super_admin: false,
    modules: { proxies: 'manage' },
    flags: [],
    settings: {} as MeAccess['settings'],
    custom_menu_items: []
  }
  access.loaded = true
  list.mockResolvedValue({
    items: [
      { id: 9, name: 'a', protocol: 'http', host: 'a.example', port: 80, status: 'active' },
      { id: 11, name: 'b', protocol: 'http', host: 'b.example', port: 80, status: 'active' }
    ],
    total: 2,
    pages: 1
  })
  getAllWithCount.mockResolvedValue([])
  batchDelete.mockResolvedValue({ deleted_ids: [9, 11], skipped: [] })
})
afterEach(() => wrapper?.unmount())

describe('ProxiesView selection', () => {
  it('deletes the rows picked in the shared table from the narrow actions menu', async () => {
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="pick"]').trigger('click')
    expect(wrapper.get('[data-test="pick"]').text()).toBe('9,11')

    await wrapper.get('[data-test="proxy-more-actions"]').trigger('click')
    await wrapper.findAll('[role="menuitem"]').find(item => item.text() === 'admin.proxies.batchDeleteAction')!.trigger('click')
    await wrapper.get('[data-dialog="admin.proxies.batchDelete"]').trigger('click')
    await flushPromises()

    expect(batchDelete).toHaveBeenCalledWith([9, 11])
    expect(wrapper.get('[data-test="pick"]').text()).toBe('')
  })

  it('closes the narrow actions menu on Escape and gives focus back to its button (7p-4)', async () => {
    wrapper = mountView()
    document.body.appendChild(wrapper.element)
    await flushPromises()

    const more = wrapper.get('[data-test="proxy-more-actions"]')
    await more.trigger('click')
    expect(wrapper.find('[role="menu"]').exists()).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await flushPromises()

    expect(wrapper.find('[role="menu"]').exists()).toBe(false)
    expect(document.activeElement).toBe(more.element)
  })
})
