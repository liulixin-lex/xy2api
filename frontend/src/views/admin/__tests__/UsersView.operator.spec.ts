import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import type { AdminUser } from '@/types'
import type { MeAccess } from '@/types/adminAccess'
import { useAdminAccessStore } from '@/stores/adminAccess'
import { useAuthStore } from '@/stores/auth'
import UsersView from '../UsersView.vue'

const api = vi.hoisted(() => ({
  list: vi.fn(),
  getPlatformQuotas: vi.fn(),
  batchUpdateStatus: vi.fn(),
  getAllGroups: vi.fn(),
  getBatchUsersUsage: vi.fn(),
  listEnabledDefinitions: vi.fn(),
  getBatchUserAttributes: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { list: api.list, getPlatformQuotas: api.getPlatformQuotas, batchUpdateStatus: api.batchUpdateStatus },
    groups: { getAll: api.getAllGroups },
    dashboard: { getBatchUsersUsage: api.getBatchUsersUsage },
    userAttributes: { listEnabledDefinitions: api.listEnabledDefinitions, getBatchUserAttributes: api.getBatchUserAttributes }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const user = (overrides: Partial<AdminUser>): AdminUser => ({
  id: 42,
  username: 'member',
  email: 'member@example.com',
  role: 'user',
  balance: 0,
  concurrency: 1,
  status: 'active',
  allowed_groups: [],
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-04-17T00:00:00Z',
  updated_at: '2026-04-17T00:00:00Z',
  notes: '',
  current_concurrency: 0,
  ...overrides
})

const DataTableStub = {
  props: ['columns', 'data', 'selectedKeys'],
  emits: ['update:selectedKeys'],
  template: `
    <div>
      <div data-test="selected-keys">{{ (selectedKeys || []).join(',') }}</div>
      <button data-test="select-page" @click="$emit('update:selectedKeys', data.map(row => row.id))">select page</button>
      <div v-for="row in data" :key="row.id" :data-test="'row-' + row.id">
        <slot name="cell-balance" :value="row.balance" :row="row" />
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}

function mountView() {
  return mount(UsersView, {
    global: {
      plugins: [getActivePinia()!],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: {
          props: ['show'],
          emits: ['confirm', 'cancel'],
          template: '<button v-if="show" data-test="confirm-dialog" @click="$emit(\'confirm\')">confirm</button>'
        },
        EmptyState: true,
        GroupBadge: true,
        Select: true,
        UserAttributesConfigModal: true,
        UserConcurrencyCell: true,
        UserCreateModal: true,
        UserEditModal: true,
        BulkEditUserModal: true,
        UserPlatformQuotaModal: true,
        UserApiKeysModal: true,
        UserAllowedGroupsModal: true,
        UserBalanceModal: true,
        UserBalanceHistoryModal: true,
        GroupReplaceModal: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

function signIn(id: number, role: AdminUser['role'], access?: Partial<MeAccess>) {
  useAuthStore().user = user({ id, role, email: `${role}@example.com` })
  if (!access) return
  const store = useAdminAccessStore()
  store.access = {
    role: 'operator',
    is_super_admin: false,
    modules: {},
    flags: [],
    settings: {} as MeAccess['settings'],
    custom_menu_items: [],
    ...access
  }
  store.loaded = true
}

async function openMenu(wrapper: ReturnType<typeof mountView>, id: number) {
  const trigger = wrapper.get(`[data-test="row-${id}"]`).findAll('button').find((b) => b.classes().includes('action-menu-trigger'))
  if (!trigger) throw new Error(`menu trigger not found for ${id}`)
  await trigger.trigger('click')
}

const menuTexts = (wrapper: ReturnType<typeof mountView>) =>
  wrapper.findAll('.action-menu-content button').map((b) => b.text())

describe('UsersView row actions by role', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
    api.getAllGroups.mockResolvedValue([])
    api.getBatchUsersUsage.mockResolvedValue({ stats: {} })
    api.listEnabledDefinitions.mockResolvedValue([])
    api.getBatchUserAttributes.mockResolvedValue({ attributes: {} })
    api.getPlatformQuotas.mockResolvedValue({ platform_quotas: [] })
    api.batchUpdateStatus.mockResolvedValue({ affected: 1, skipped: 0 })
  })
  it('shows deposit, withdraw, allowed groups and platform quotas on an admin row to a super admin', async () => {
    signIn(1, 'admin')
    api.list.mockResolvedValue({
      items: [user({ id: 1, role: 'admin', email: 'root@example.com' }), user({ id: 2, role: 'admin', email: 'second@example.com' })],
      total: 2, page: 1, page_size: 20, pages: 1
    })
    const wrapper = mountView()
    await flushPromises()

    for (const id of [1, 2]) {
      expect(wrapper.get(`[data-test="row-${id}"]`).text()).toContain('admin.users.deposit')
      await openMenu(wrapper, id)
      expect(menuTexts(wrapper)).toEqual(expect.arrayContaining([
        'admin.users.groups',
        'admin.users.deposit',
        'admin.users.withdraw',
        'admin.users.platformQuota.menuItem'
      ]))
      // Status toggle and delete keep their old "not an admin" guard.
      expect(menuTexts(wrapper)).not.toContain('common.delete')
      await openMenu(wrapper, id)
    }
    wrapper.unmount()
  })

  it('skips platform quotas and bulk selection of admin, operator and own rows for an operator', async () => {
    localStorage.setItem('user-hidden-columns', '[]')
    localStorage.setItem('user-column-settings-version', '3')
    signIn(7, 'operator', { modules: { users: 'manage' } })
    api.list.mockResolvedValue({
      items: [
        user({ id: 1, role: 'admin', email: 'root@example.com' }),
        user({ id: 7, role: 'operator', email: 'me@example.com' }),
        user({ id: 8, role: 'operator', email: 'other@example.com' }),
        user({ id: 42, role: 'user' })
      ],
      total: 4, page: 1, page_size: 20, pages: 1
    })
    const wrapper = mountView()
    await flushPromises()
    await new Promise((resolve) => setTimeout(resolve, 80))
    await flushPromises()

    expect(api.getPlatformQuotas.mock.calls.map(([id]) => id)).toEqual([42])

    await wrapper.get('[data-test="select-page"]').trigger('click')
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('42')
    await wrapper.get('[data-test="bulk-disable-users"]').trigger('click')
    await wrapper.get('[data-test="confirm-dialog"]').trigger('click')
    await flushPromises()
    expect(api.batchUpdateStatus).toHaveBeenCalledWith({ user_ids: [42], status: 'disabled' })
    wrapper.unmount()
  })
})


describe('UsersView operator attribute batches', () => {
  it.each([false, true])('only reads readable attribute rows and skips an empty batch (%s)', async (empty) => {
    localStorage.setItem('user-hidden-columns', '[]')
    localStorage.setItem('user-column-settings-version', '3')
    signIn(7, 'operator', { modules: { users: 'view' } })
    api.getBatchUserAttributes.mockClear().mockResolvedValue({ attributes: {} })
    api.listEnabledDefinitions.mockResolvedValue([{ id: 1, name: 'Team', enabled: true, type: 'text' }])
    api.list.mockResolvedValue({ items: [user({ id: 1, role: 'admin' }), user({ id: 7, role: 'operator' }), user({ id: 8, role: 'operator' }), ...(empty ? [] : [user({ id: 42 })])], total: 4 })
    const wrapper = mountView()
    await flushPromises()
    await new Promise(resolve => setTimeout(resolve, 80))
    await flushPromises()
    if (empty) expect(api.getBatchUserAttributes).not.toHaveBeenCalled()
    else expect(api.getBatchUserAttributes).toHaveBeenCalledWith([42])
    wrapper.unmount()
  })
})
