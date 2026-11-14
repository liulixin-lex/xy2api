import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import { useAdminAccessStore } from '@/stores/adminAccess'
import type { MeAccess } from '@/types/adminAccess'
import ProxiesView from '../ProxiesView.vue'

const { list, getAllWithCount } = vi.hoisted(() => ({ list: vi.fn(), getAllWithCount: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, getAllWithCount } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const mountView = () => shallowMount(ProxiesView, {
  attachTo: document.body,
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="table" /></div>' },
    DataTable: { props: ['data'], template: '<div v-for="row in data" :key="row.id" data-test="address"><slot name="cell-address" :row="row" /></div>' },
    Teleport: false
  } }
})

const size = { innerWidth: window.innerWidth, innerHeight: window.innerHeight }
let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
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
    items: [{ id: 9, name: 'a', protocol: 'http', host: 'proxy.example', port: 8080, username: 'u', password: 'p', status: 'active' }],
    total: 1,
    pages: 1
  })
  getAllWithCount.mockResolvedValue([])
  Object.assign(window, { innerWidth: 390, innerHeight: 844 })
})
afterEach(() => {
  wrapper?.unmount()
  Object.assign(window, size)
  vi.restoreAllMocks()
})

describe('ProxiesView copy-format menu', () => {
  it('opens in the body and stays whole on a 390px screen beside a button at its right edge (7r-4)', async () => {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      const rect = this.matches('[data-test="proxy-copy-menu"]')
        ? { left: 0, top: 0, right: 0, bottom: 0, width: 220, height: 90 }
        : { left: 350, top: 280, right: 366, bottom: 296, width: 16, height: 16 }
      return { ...rect, x: rect.left, y: rect.top, toJSON: () => rect } as DOMRect
    })
    wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="address"] button').trigger('contextmenu')
    await flushPromises()

    const menu = document.querySelector<HTMLElement>('[data-test="proxy-copy-menu"]')!
    expect(menu.parentElement).toBe(document.body)
    expect(menu.querySelectorAll('[role="menuitem"]')).toHaveLength(3)
    // 390 - 220 - 8: the menu's right edge 8px inside the screen, under the button (296 + 4).
    expect(menu.style.left).toBe('162px')
    expect(menu.style.top).toBe('300px')
  })
})
