import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminUser } from '@/types'
import UsersView from '../UsersView.vue'

/** A bulk action, from the toolbar's bulk menu (12.9-5), opened first when it is closed. */
const bulkItem = async (wrapper: ReturnType<typeof mount>, test: string) => {
  if (!wrapper.find('[data-test="bulk-menu"]').exists()) await wrapper.get('[data-test="bulk-actions"]').trigger('click')
  return wrapper.get(`[data-test="${test}"]`)
}

const {
  listUsers,
  toggleStatus,
  deleteUser,
  batchUpdateStatus,
  showError,
  showSuccess,
  showInfo,
  getAllGroups,
  getBatchUsersUsage,
  listEnabledDefinitions,
  getBatchUserAttributes
} = vi.hoisted(() => ({
  listUsers: vi.fn(),
  toggleStatus: vi.fn(),
  deleteUser: vi.fn(),
  batchUpdateStatus: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn(),
  getAllGroups: vi.fn(),
  getBatchUsersUsage: vi.fn(),
  listEnabledDefinitions: vi.fn(),
  getBatchUserAttributes: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      list: listUsers,
      toggleStatus,
      delete: deleteUser,
      batchUpdateStatus
    },
    groups: {
      getAll: getAllGroups,
      getAllIncludingInactive: getAllGroups
    },
    dashboard: {
      getBatchUsersUsage
    },
    userAttributes: {
      listEnabledDefinitions,
      getBatchUserAttributes
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: { count?: number }) =>
        params?.count === undefined ? key : `${key}:${params.count}`
    })
  }
})

const createAdminUser = (overrides: Partial<AdminUser> = {}): AdminUser => ({
  id: 42,
  username: 'scoped-user',
  email: 'scoped@example.com',
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
  last_active_at: '2026-04-16T02:00:00Z',
  last_used_at: '2026-04-17T02:00:00Z',
  current_concurrency: 0,
  ...overrides
})

const tableScrollToTop = vi.fn()
const DataTableStub = {
  props: ['columns', 'data', 'selectedKeys'],
  emits: ['sort', 'update:selectedKeys'],
  methods: { scrollToTop: tableScrollToTop },
  template: `
    <div>
      <div data-test="columns">{{ columns.map(col => col.key).join(',') }}</div>
      <div data-test="row-order">{{ data.map(row => row.email).join(',') }}</div>
      <div data-test="row-state">{{ data.map(row => [row.id, row.status, row.current_concurrency].join(':')).join(',') }}</div>
      <div data-test="selected-keys">{{ (selectedKeys || []).join(',') }}</div>
      <button data-test="sort-last-used" @click="$emit('sort', 'last_used_at', 'desc')">sort</button>
      <button
        v-for="row in data"
        :key="'select-' + row.id"
        :data-test="'select-' + row.id"
        @click="$emit('update:selectedKeys', Array.from(new Set([...(selectedKeys || []), row.id])))"
      >
        select
      </button>
      <template v-for="col in columns" :key="col.key">
        <slot :name="'header-' + col.key" :column="col" />
      </template>
      <div v-for="row in data" :key="row.id">
        <slot name="cell-email" :value="row.email" :row="row" />
        <slot name="cell-last_used_at" :value="row.last_used_at" :row="row" />
        <div :data-test="'actions-' + row.id"><slot name="cell-actions" :row="row" /></div>
      </div>
    </div>
  `
}

const PaginationStub = {
  emits: ['update:page'],
  template: '<button data-test="next-page" @click="$emit(\'update:page\', 2)">next</button>'
}

const BulkEditUserModalStub = {
  props: ['show', 'selectedIds'],
  emits: ['close', 'success'],
  template: `
    <div v-if="show" data-test="bulk-modal">
      <span data-test="bulk-modal-ids">{{ selectedIds.join(',') }}</span>
      <button data-test="bulk-success" @click="$emit('success', selectedIds.length)">success</button>
    </div>
  `
}

const mountBulkDeleteView = () => mount(UsersView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: {
        template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
      },
      DataTable: DataTableStub,
      Pagination: PaginationStub,
      ConfirmDialog: {
        props: ['show', 'message'],
        emits: ['confirm', 'cancel'],
        template: `<div v-if="show" data-test="delete-dialog">
          <span>{{ message }}</span>
          <button data-test="confirm-delete" @click="$emit('confirm')">confirm</button>
          <button data-test="cancel-delete" @click="$emit('cancel')">cancel</button>
        </div>`
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

const getToggleStatusButton = (wrapper: ReturnType<typeof mountBulkDeleteView>, id: number) => {
  const button = wrapper
    .get(`[data-test="actions-${id}"]`)
    .findAll('button')
    .find((candidate) => /admin\.users\.(disable|enable)$/.test(candidate.text()))
  if (!button) throw new Error(`toggle status button not found for user ${id}`)
  return button
}

describe('admin UsersView', () => {
  beforeEach(() => {
    vi.useRealTimers()
    localStorage.clear()

    listUsers.mockReset()
    toggleStatus.mockReset()
    deleteUser.mockReset()
    batchUpdateStatus.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    showInfo.mockReset()
    getAllGroups.mockReset()
    getBatchUsersUsage.mockReset()
    listEnabledDefinitions.mockReset()
    getBatchUserAttributes.mockReset()

    listUsers.mockResolvedValue({
      items: [createAdminUser()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getAllGroups.mockResolvedValue([])
    getBatchUsersUsage.mockResolvedValue({ stats: {} })
    listEnabledDefinitions.mockResolvedValue([])
    getBatchUserAttributes.mockResolvedValue({ values: {} })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('cancels bulk deletion without deleting or clearing selected users', async () => {
    const wrapper = mountBulkDeleteView()
    await flushPromises()

    // The bulk menu's button is disabled until a row is ticked (12.9-5).
    expect(wrapper.get('[data-test="bulk-actions"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="select-42"]').trigger('click')
    expect((await bulkItem(wrapper, 'bulk-delete-users')).attributes('disabled')).toBeUndefined()
    await (await bulkItem(wrapper, 'bulk-delete-users')).trigger('click')
    expect(wrapper.get('[data-test="delete-dialog"]').text()).toContain('admin.users.bulkDelete.confirm:1')
    expect(deleteUser).not.toHaveBeenCalled()

    await wrapper.get('[data-test="cancel-delete"]').trigger('click')
    expect(wrapper.find('[data-test="delete-dialog"]').exists()).toBe(false)
    expect(deleteUser).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('42')
    wrapper.unmount()
  })

  it.each([
    { failedIds: [], remaining: '', deleted: 2 },
    { failedIds: [43], remaining: '43', deleted: 1 },
    { failedIds: [42, 43], remaining: '42,43', deleted: 0 }
  ])('deletes across pages and retains failures: $remaining', async ({ failedIds, remaining, deleted }) => {
    listUsers.mockImplementation(async (page: number) => ({
      items: [createAdminUser({ id: page === 2 ? 43 : 42 })],
      total: 2, page, page_size: 20, pages: 2
    }))
    deleteUser.mockImplementation(async (id: number) => {
      if (failedIds.includes(id)) throw new Error('Cannot delete user')
    })
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    await wrapper.get('[data-test="select-42"]').trigger('click')
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="select-43"]').trigger('click')
    await (await bulkItem(wrapper, 'bulk-delete-users')).trigger('click')
    expect(deleteUser).not.toHaveBeenCalled()

    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(deleteUser.mock.calls).toEqual([[42], [43]])
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe(remaining)
    expect(wrapper.find('[data-test="delete-dialog"]').exists()).toBe(false)
    if (deleted) {
      expect(showSuccess).toHaveBeenCalledWith(`admin.users.bulkDelete.success:${deleted}`)
      expect(listUsers.mock.lastCall?.[0]).toBe(1)
    } else {
      expect(showSuccess).not.toHaveBeenCalled()
    }
    if (failedIds.length) expect(showError).toHaveBeenCalledWith(`admin.users.bulkDelete.failed:${failedIds.length}`)
    else expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('takes deleted users off the page at once, before the reload answers (8.5a)', async () => {
    listUsers.mockResolvedValue({
      items: [createAdminUser({ id: 42, email: 'a@example.com' }), createAdminUser({ id: 43, email: 'b@example.com' })],
      total: 2, page: 1, page_size: 20, pages: 1
    })
    deleteUser.mockResolvedValue(undefined)
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    await wrapper.get('[data-test="select-42"]').trigger('click')
    await (await bulkItem(wrapper, 'bulk-delete-users')).trigger('click')
    listUsers.mockReturnValue(new Promise(() => {})) // the reload after the delete never answers here
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(deleteUser.mock.calls).toEqual([[42]])
    expect(wrapper.get('[data-test="row-order"]').text()).toBe('b@example.com')
    wrapper.unmount()
  })

  it('deletes the confirmed selection while preserving users selected during deletion', async () => {
    listUsers.mockResolvedValue({
      items: [createAdminUser({ id: 42 }), createAdminUser({ id: 43 })],
      total: 2, page: 1, page_size: 20, pages: 1
    })
    let finishDelete!: () => void
    deleteUser.mockImplementation(() => new Promise<void>(resolve => { finishDelete = resolve }))
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    await wrapper.get('[data-test="select-42"]').trigger('click')
    await (await bulkItem(wrapper, 'bulk-delete-users')).trigger('click')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    expect((await bulkItem(wrapper, 'bulk-delete-users')).attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="select-43"]').trigger('click')
    finishDelete()
    await flushPromises()

    expect(deleteUser.mock.calls).toEqual([[42]])
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('43')
    wrapper.unmount()
  })

  it('shows the live indicator and, after the 10 s check finds a new user, the changes notice that takes it in', async () => {
    vi.useFakeTimers()
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    expect(wrapper.get('[data-test="live-status"]').attributes('data-status')).toBe('live')
    expect(wrapper.find('[data-test="pending-changes"]').exists()).toBe(false)

    listUsers.mockResolvedValue({
      items: [createAdminUser({ id: 43, email: 'new@example.com' }), createAdminUser()],
      total: 2, page: 1, page_size: 20, pages: 1
    })
    await vi.advanceTimersByTimeAsync(10_000)
    expect(wrapper.get('[data-test="row-order"]').text()).toBe('scoped@example.com') // counted, not shown yet
    const notice = wrapper.get('[data-test="pending-changes"]')
    expect(notice.text()).toContain('common.pendingChanges.changes')

    await notice.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="row-order"]').text()).toBe('new@example.com,scoped@example.com')
    wrapper.unmount()
  })

  it('disables users selected across pages in one request, then clears the selection and reloads', async () => {
    listUsers.mockImplementation(async (page: number) => ({
      items: [createAdminUser({ id: page === 2 ? 43 : 42 })],
      total: 2, page, page_size: 20, pages: 2
    }))
    batchUpdateStatus.mockResolvedValue({ affected: 1, skipped: 1 })
    const wrapper = mountBulkDeleteView()
    await flushPromises()

    expect(wrapper.get('[data-test="bulk-actions"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="select-42"]').trigger('click')
    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="select-43"]').trigger('click')
    await (await bulkItem(wrapper, 'bulk-disable-users')).trigger('click')
    expect(wrapper.get('[data-test="delete-dialog"]').text()).toContain('admin.users.bulkStatus.disableConfirm:2')
    expect(batchUpdateStatus).not.toHaveBeenCalled()

    const listCallsBeforeConfirm = listUsers.mock.calls.length
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(batchUpdateStatus.mock.calls).toEqual([[{ user_ids: [42, 43], status: 'disabled' }]])
    expect(showSuccess).toHaveBeenCalledWith('admin.users.bulkStatus.disableSuccess:1')
    expect(showInfo).toHaveBeenCalledWith('admin.users.bulkStatus.skipped:1')
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('')
    expect(listUsers.mock.calls.length).toBe(listCallsBeforeConfirm + 1)
    expect(deleteUser).not.toHaveBeenCalled()
    expect(toggleStatus).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('names every secondary action whose text goes below 1024px; "create" keeps its text (7o-3)', async () => {
    listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42 })], total: 1, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    await wrapper.get('[data-test="select-42"]').trigger('click')

    // Attributes and the bulk menu; refresh, filter and column settings are icons at every width (12.9-2).
    const labels = wrapper.findAll('.table-toolbar__label')
    expect(labels).toHaveLength(2)
    for (const label of labels) {
      expect(label.element.closest('button')!.getAttribute('aria-label')).toBe(label.text())
    }
    const create = wrapper.findAll('button').find((button) => button.text() === 'admin.users.createUser')!
    expect(create.find('.table-toolbar__label').exists()).toBe(false)
    wrapper.unmount()
  })

  it('folds the four bulk actions into one menu, disabled until a row is ticked, the count on its button (12.9-5)', async () => {
    listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42 })], total: 1, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    const button = wrapper.get('[data-test="bulk-actions"]')
    expect(button.attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="bulk-count"]').classes()).toContain('is-empty')

    await wrapper.get('[data-test="select-42"]').trigger('click')
    expect(button.attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="bulk-count"]').text()).toBe('1')
    await button.trigger('click')
    expect(wrapper.findAll('[data-test="bulk-menu"] [role="menuitem"]').map((item) => item.text()))
      .toEqual(['admin.users.bulkLimits.label', 'admin.users.enable', 'admin.users.disable', 'common.delete'])

    // A sub-layer of the overlay stack: Escape closes it.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('[data-test="bulk-menu"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // Mobile list plan: the frozen identity is the address with the user name under it, no avatar; a cut line shows
  // whole in the tip a tap opens (7o-5).
  it('shows the address and the user name in the identity, whole in the tip a tap shows when cut (7o-5)', async () => {
    const email = 'a-very-long-address-for-acceptance@example.test'
    listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42, email, username: 'acceptance' })], total: 1, page: 1, page_size: 20, pages: 1 })
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    document.body.appendChild(wrapper.element)
    const cell = wrapper.get('[data-test="user-email"]')
    const primary = cell.get('.table-identity__primary')
    expect(primary.text()).toBe(email)
    expect(cell.get('.table-identity__secondary').text()).toBe('acceptance')
    Object.defineProperty(primary.element, 'scrollWidth', { configurable: true, value: 400 })
    Object.defineProperty(primary.element, 'clientWidth', { configurable: true, value: 160 })
    await cell.trigger('click')
    expect(document.getElementById('sq-tip')!.textContent).toBe(`${email}\nacceptance`)
    wrapper.unmount()
  })

  it('keeps the selection when bulk enabling is cancelled or fails', async () => {
    batchUpdateStatus.mockRejectedValue({ status: 500, message: 'database unavailable' })
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    await wrapper.get('[data-test="select-42"]').trigger('click')

    await (await bulkItem(wrapper, 'bulk-enable-users')).trigger('click')
    expect(wrapper.get('[data-test="delete-dialog"]').text()).toContain('admin.users.bulkStatus.enableConfirm:1')
    await wrapper.get('[data-test="cancel-delete"]').trigger('click')
    expect(wrapper.find('[data-test="delete-dialog"]').exists()).toBe(false)
    expect(batchUpdateStatus).not.toHaveBeenCalled()

    await (await bulkItem(wrapper, 'bulk-enable-users')).trigger('click')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(batchUpdateStatus.mock.calls).toEqual([[{ user_ids: [42], status: 'active' }]])
    expect(showError).toHaveBeenCalledWith('database unavailable')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('42')
    expect((await bulkItem(wrapper, 'bulk-enable-users')).attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('updates only the toggled row in place without reloading the list', async () => {
    listUsers.mockResolvedValue({
      items: [createAdminUser({ id: 42, current_concurrency: 3 }), createAdminUser({ id: 43 })],
      total: 2, page: 1, page_size: 20, pages: 1
    })
    toggleStatus.mockResolvedValue(
      createAdminUser({ id: 42, status: 'disabled', current_concurrency: undefined })
    )
    const wrapper = mountBulkDeleteView()
    await flushPromises()
    expect(wrapper.get('[data-test="row-state"]').text()).toBe('42:active:3,43:active:0')

    await getToggleStatusButton(wrapper, 42).trigger('click')
    await flushPromises()

    expect(toggleStatus.mock.calls).toEqual([[42, 'disabled']])
    expect(listUsers).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="row-state"]').text()).toBe('42:disabled:3,43:active:0')
    expect(getToggleStatusButton(wrapper, 42).text()).toBe('admin.users.enable')
    expect(showSuccess).toHaveBeenCalledWith('admin.users.userDisabled')
    wrapper.unmount()
  })

  it('keeps the row untouched when toggling status fails', async () => {
    toggleStatus.mockRejectedValue(new Error('boom'))
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountBulkDeleteView()
    await flushPromises()

    await getToggleStatusButton(wrapper, 42).trigger('click')
    await flushPromises()

    expect(listUsers).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="row-state"]').text()).toBe('42:active:0')
    expect(showError).toHaveBeenCalledWith('admin.users.failedToToggle')
    expect(showSuccess).not.toHaveBeenCalled()
    errorSpy.mockRestore()
    wrapper.unmount()
  })

  it('keeps the toggled status when a list answer asked for before the toggle settled arrives, without reloading', async () => {
    let finishToggle!: (user: AdminUser) => void
    toggleStatus.mockImplementation(() => new Promise<AdminUser>(resolve => { finishToggle = resolve }))
    const wrapper = mountBulkDeleteView()
    await flushPromises()

    await getToggleStatusButton(wrapper, 42).trigger('click')
    expect(wrapper.get('[data-test="row-state"]').text()).toBe('42:disabled:0') // at once
    await wrapper.get('[data-test="sort-last-used"]').trigger('click') // a reload that still reads the old status
    await flushPromises()
    expect(listUsers).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="row-state"]').text()).toBe('42:disabled:0') // the locked field keeps the new status

    finishToggle(createAdminUser({ status: 'disabled' }))
    await flushPromises()
    expect(listUsers).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="row-state"]').text()).toBe('42:disabled:0')
    wrapper.unmount()
  })

  it('shows active, used, and created activity columns in order and requests last_used_at sort', async () => {
    const wrapper = mount(UsersView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          EmptyState: true,
          GroupBadge: true,
          Select: true,
          UserAttributesConfigModal: true,
          UserConcurrencyCell: true,
          UserCreateModal: true,
          UserEditModal: true,
          BulkEditUserModal: BulkEditUserModalStub,
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

    await flushPromises()

    const columns = wrapper.get('[data-test="columns"]').text()
    const visibleColumns = columns.split(',')
    expect(visibleColumns.slice(-4, -1)).toEqual(['last_active_at', 'last_used_at', 'created_at'])
    expect(visibleColumns).not.toContain('last_login_at')

    await wrapper.get('[data-test="sort-last-used"]').trigger('click')
    await flushPromises()

    expect(listUsers).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({
        sort_by: 'last_used_at',
        sort_order: 'desc'
      }),
      expect.any(Object)
    )
  })

  it('clears usage current-page sort when switching to last_used_at server sort', async () => {
    vi.useFakeTimers()
    localStorage.setItem('user-column-settings-version', '3')
    localStorage.setItem(
      'user-hidden-columns',
      JSON.stringify([
        'notes',
        'groups',
        'subscriptions',
        'concurrency',
        'usage_anthropic',
        'usage_openai',
        'usage_gemini',
        'usage_antigravity',
        'balance_platform_quota'
      ])
    )

    listUsers.mockResolvedValue({
      items: [
        createAdminUser({ id: 1, email: 'last-used-first@example.com' }),
        createAdminUser({ id: 2, email: 'usage-first@example.com' })
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getBatchUsersUsage.mockResolvedValue({
      stats: {
        1: { user_id: 1, today_actual_cost: 1, total_actual_cost: 1, by_platform: [] },
        2: { user_id: 2, today_actual_cost: 9, total_actual_cost: 9, by_platform: [] }
      }
    })

    const wrapper = mount(UsersView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          EmptyState: true,
          GroupBadge: true,
          Select: true,
          UserAttributesConfigModal: true,
          UserConcurrencyCell: true,
          UserCreateModal: true,
          UserEditModal: true,
          BulkEditUserModal: BulkEditUserModalStub,
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

    await flushPromises()
    await vi.advanceTimersByTimeAsync(50)
    await flushPromises()

    expect(wrapper.get('[data-test="row-order"]').text()).toBe('last-used-first@example.com,usage-first@example.com')

    tableScrollToTop.mockClear()
    await wrapper.get('[data-test="usage-sort-trigger-usage"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="usage-sort-usage-today"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="row-order"]').text()).toBe('usage-first@example.com,last-used-first@example.com')
    expect(tableScrollToTop).toHaveBeenCalledTimes(1) // a usage sort starts from the first row (8.4a)
    expect(localStorage.getItem('admin-users-usage-sort')).toContain('"key":"usage"')

    await wrapper.get('[data-test="sort-last-used"]').trigger('click')
    await flushPromises()

    expect(localStorage.getItem('admin-users-usage-sort')).toBeNull()
    expect(wrapper.get('[data-test="row-order"]').text()).toBe('last-used-first@example.com,usage-first@example.com')
    expect(listUsers).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({
        sort_by: 'last_used_at',
        sort_order: 'desc'
      }),
      expect.any(Object)
    )
  })

  it('keeps selected user IDs across pages and clears them after a successful bulk update', async () => {
    let refreshed = false
    listUsers.mockImplementation(async (page: number) => {
      const user = page === 2
        ? createAdminUser({
            id: 43,
            email: refreshed ? 'refreshed-page-two@example.com' : 'page-two@example.com'
          })
        : createAdminUser({ id: 42, email: 'page-one@example.com' })
      return {
        items: [user],
        total: 2,
        page,
        page_size: 20,
        pages: 2
      }
    })

    const wrapper = mount(UsersView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: PaginationStub,
          ConfirmDialog: true,
          EmptyState: true,
          GroupBadge: true,
          Select: true,
          UserAttributesConfigModal: true,
          UserConcurrencyCell: true,
          UserCreateModal: true,
          UserEditModal: true,
          BulkEditUserModal: BulkEditUserModalStub,
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

    await flushPromises()

    expect(wrapper.get('[data-test="bulk-actions"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="select-42"]').trigger('click')
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('42')
    expect((await bulkItem(wrapper, 'bulk-edit-limits')).attributes('disabled')).toBeUndefined()

    await wrapper.get('[data-test="next-page"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('42')

    await wrapper.get('[data-test="select-43"]').trigger('click')
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('42,43')

    await (await bulkItem(wrapper, 'bulk-edit-limits')).trigger('click')
    expect(wrapper.get('[data-test="bulk-modal-ids"]').text()).toBe('42,43')

    const callsBeforeSuccess = listUsers.mock.calls.length
    refreshed = true
    await wrapper.get('[data-test="bulk-success"]').trigger('click')
    await flushPromises()

    expect(listUsers.mock.calls.length).toBeGreaterThan(callsBeforeSuccess)
    expect(wrapper.get('[data-test="row-order"]').text()).toBe('refreshed-page-two@example.com')
    expect(wrapper.get('[data-test="bulk-actions"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('')
  })

  // Mobile list plan: below 768px a memory of its own, showing balance, status and last use until changed; the
  // wide memory (the page's old key) is neither read for it nor written.
  it('shows the phone defaults below 768px, its own memory, and the column sheet', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: query !== '(min-width: 768px)' && query !== '(min-width: 1024px)',
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {}
    })) as unknown as typeof window.matchMedia
    localStorage.setItem('user-hidden-columns', JSON.stringify(['notes']))
    try {
      listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42 })], total: 1, page: 1, page_size: 20, pages: 1 })
      const wrapper = mountBulkDeleteView()
      await flushPromises()
      expect(wrapper.get('[data-test="columns"]').text()).toBe('email,balance,status,last_used_at,actions')
      await wrapper.get('[data-test="users-columns"]').trigger('click')
      await flushPromises()
      const role = wrapper.findAll('[data-test="column-sheet"] [data-test="column-setting"]').find((el) => el.text().includes('admin.users.columns.role'))
      await role!.trigger('click')
      await flushPromises()
      expect(wrapper.get('[data-test="columns"]').text()).toBe('email,role,balance,status,last_used_at,actions')
      expect(JSON.parse(localStorage.getItem('user-hidden-columns-phone-v1')!)).not.toContain('role')
      expect(JSON.parse(localStorage.getItem('user-hidden-columns')!)).toEqual(['notes'])
      await wrapper.get('[data-test="column-sheet"] [data-test="reset-columns"]').trigger('click')
      await flushPromises()
      expect(wrapper.get('[data-test="columns"]').text()).toBe('email,balance,status,last_used_at,actions')
      expect(localStorage.getItem('user-hidden-columns-phone-v1')).toBeNull()
      wrapper.unmount()
    } finally {
      window.matchMedia = matchMedia
      localStorage.removeItem('user-hidden-columns')
    }
  })

  it('filters from the phone sheet, shows the choice as a tag, and keeps it shown from 768px', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: query !== '(min-width: 768px)' && query !== '(min-width: 1024px)',
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {}
    })) as unknown as typeof window.matchMedia
    try {
      listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42 })], total: 1, page: 1, page_size: 20, pages: 1 })
      const wrapper = mountBulkDeleteView()
      await flushPromises()
      expect(wrapper.get('[data-test="users-count"]').text()).toBe('common.table.count')
      await wrapper.get('[data-test="toolbar-filter-button"]').trigger('click')
      await flushPromises()
      const role = wrapper.get('[data-test="filter-sheet"] [data-filter="role"]').findComponent({ name: 'Select' })
      role.vm.$emit('update:modelValue', 'operator')
      await flushPromises()
      expect(listUsers).toHaveBeenLastCalledWith(1, 20, expect.objectContaining({ role: 'operator' }), expect.any(Object))
      expect(wrapper.get('[data-test="toolbar-filter-count"]').text()).toBe('1')
      const tag = wrapper.get('[data-key="role"]')
      expect(tag.text()).toContain('admin.users.operator')
      expect(JSON.parse(localStorage.getItem('user-visible-filters')!)).toContain('role')
      await tag.get('button').trigger('click')
      await flushPromises()
      expect(wrapper.find('[data-key="role"]').exists()).toBe(false)
      expect(listUsers).toHaveBeenLastCalledWith(1, 20, expect.not.objectContaining({ role: 'operator' }), expect.any(Object))
      wrapper.unmount()
    } finally {
      window.matchMedia = matchMedia
      localStorage.removeItem('user-visible-filters')
      localStorage.removeItem('user-filter-values')
    }
  })

  it('keeps only "⋯" in a phone row, editing and enabling leading its menu', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: query !== '(min-width: 768px)' && query !== '(min-width: 1024px)',
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {}
    })) as unknown as typeof window.matchMedia
    try {
      listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42, email: 'phone@example.test' })], total: 1, page: 1, page_size: 20, pages: 1 })
      const wrapper = mountBulkDeleteView()
      await flushPromises()
      const actions = wrapper.get('[data-test="actions-42"]')
      expect(actions.findAll('button')).toHaveLength(1)
      const more = actions.get('[data-test="row-more"]')
      expect(more.attributes('aria-label')).toBe('common.more phone@example.test')
      await more.trigger('click')
      await flushPromises()
      const items = wrapper.findAll('.action-menu-content button').map((el) => el.attributes('data-test') ?? '')
      expect(items.slice(0, 2)).toEqual(['menu-edit', 'menu-toggle-status'])
      await wrapper.get('[data-test="menu-toggle-status"]').trigger('click')
      await flushPromises()
      expect(toggleStatus).toHaveBeenCalled()
      wrapper.unmount()
    } finally {
      window.matchMedia = matchMedia
    }
  })

  // Astra review: the page's own usage sort, on while its column is hidden (a phone), still fetches and orders.
  it('fetches usage and orders by it while the usage sort is on and its column hidden', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: query !== '(min-width: 768px)' && query !== '(min-width: 1024px)',
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {}
    })) as unknown as typeof window.matchMedia
    localStorage.setItem('admin-users-usage-sort', JSON.stringify({ key: 'usage', metric: 'today', order: 'desc' }))
    try {
      listUsers.mockResolvedValue({
        items: [1, 2, 3].map((id) => createAdminUser({ id, email: `u${id}@example.test` })),
        total: 3,
        page: 1,
        page_size: 20,
        pages: 1
      })
      getBatchUsersUsage.mockResolvedValue({
        stats: Object.fromEntries([1, 2, 3].map((id) => [id, { user_id: id, today_actual_cost: id, total_actual_cost: id, by_platform: [] }]))
      })
      const wrapper = mountBulkDeleteView()
      await flushPromises()
      expect(wrapper.get('[data-test="columns"]').text()).not.toContain('usage')
      expect(getBatchUsersUsage).toHaveBeenCalled()
      expect(wrapper.get('[data-test="row-order"]').text()).toBe('u3@example.test,u2@example.test,u1@example.test')
      wrapper.unmount()
    } finally {
      window.matchMedia = matchMedia
      localStorage.removeItem('admin-users-usage-sort')
    }
  })

  // Astra review: a super admin's menu (nine items, 395px) ran past a 320px-high screen; it is placed by its real
  // height and scrolls inside itself when taller than the room.
  it('places the row menu by its real height, scrolling inside when the screen is shorter', async () => {
    const proto = HTMLElement.prototype
    const scrollHeight = Object.getOwnPropertyDescriptor(proto, 'scrollHeight')
    const offsetWidth = Object.getOwnPropertyDescriptor(proto, 'offsetWidth')
    Object.defineProperty(proto, 'scrollHeight', { configurable: true, get() { return (this as HTMLElement).classList.contains('action-menu-content') ? 395 : 0 } })
    Object.defineProperty(proto, 'offsetWidth', { configurable: true, get() { return (this as HTMLElement).classList.contains('action-menu-content') ? 192 : 0 } })
    const height = window.innerHeight
    Object.defineProperty(window, 'innerHeight', { configurable: true, value: 320 })
    try {
      listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42 })], total: 1, page: 1, page_size: 20, pages: 1 })
      const wrapper = mountBulkDeleteView()
      await flushPromises()
      await wrapper.get('[data-test="actions-42"] .action-menu-trigger').trigger('click')
      await flushPromises()
      const menu = wrapper.get('.action-menu-content').element as HTMLElement
      expect(menu.style.maxHeight).toBe('304px')
      expect(menu.style.overflowY).toBe('auto')
      expect(menu.style.top).toBe('8px')
      wrapper.unmount()
    } finally {
      if (scrollHeight) Object.defineProperty(proto, 'scrollHeight', scrollHeight)
      else delete (proto as { scrollHeight?: number }).scrollHeight
      if (offsetWidth) Object.defineProperty(proto, 'offsetWidth', offsetWidth)
      else delete (proto as { offsetWidth?: number }).offsetWidth
      Object.defineProperty(window, 'innerHeight', { configurable: true, value: height })
    }
  })

  // Astra review: text typed in a phone's attribute field went with the sheet when the width crossed 768px.
  it('keeps text typed in a sheet attribute and applies it shortly after', async () => {
    vi.useFakeTimers()
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({
      matches: query !== '(min-width: 768px)' && query !== '(min-width: 1024px)',
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {}
    })) as unknown as typeof window.matchMedia
    try {
      listEnabledDefinitions.mockResolvedValue([{ id: 7, key: 'code', name: 'Code', type: 'text', enabled: true }])
      listUsers.mockResolvedValue({ items: [createAdminUser({ id: 42 })], total: 1, page: 1, page_size: 20, pages: 1 })
      const wrapper = mountBulkDeleteView()
      await flushPromises()
      await wrapper.get('[data-test="toolbar-filter-button"]').trigger('click')
      await flushPromises()
      const input = wrapper.get('[data-test="filter-sheet"] [data-filter="attr_7"] input')
      await input.setValue('xyz')
      expect(JSON.parse(localStorage.getItem('user-visible-filters')!)).toContain('attr_7')
      expect(JSON.parse(localStorage.getItem('user-filter-values')!).attributes).toEqual({ 7: 'xyz' })
      await vi.advanceTimersByTimeAsync(300)
      await flushPromises()
      expect(listUsers).toHaveBeenLastCalledWith(1, 20, expect.objectContaining({ attributes: { 7: 'xyz' } }), expect.any(Object))
      wrapper.unmount()
    } finally {
      window.matchMedia = matchMedia
      vi.useRealTimers()
      localStorage.removeItem('user-visible-filters')
      localStorage.removeItem('user-filter-values')
    }
  })

  // Decided (item 15): from 768px a filter with nothing chosen shows its name; "all" stays first in its list.
  it('names the wide filters while nothing is chosen', async () => {
    const { readFileSync } = await import('node:fs')
    const { dirname, resolve } = await import('node:path')
    const { fileURLToPath } = await import('node:url')
    const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../UsersView.vue'), 'utf8')
    for (const [field, label] of [['role', "t('admin.users.columns.role')"], ['status', "t('admin.users.columns.status')"], ['group', "t('admin.users.authorizedGroupFilter')"]]) {
      expect(source).toContain(`<template #selected="{ option }">{{ filters.${field} ? option?.label ?? filters.${field} : ${label} }}</template>`)
    }
    expect(source).toContain(`<template #selected="{ option }">{{ filters.apiKeyGroup !== null ? option?.label : t('admin.users.apiKeyGroupFilter') }}</template>`)
  })
})

