import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import UserDetail from '@/views/user/SupportTicketDetailView.vue'
import AdminDetail from '@/views/admin/SupportTicketDetailView.vue'
import UserList from '@/views/user/SupportTicketsView.vue'
import AdminList from '@/views/admin/SupportTicketsView.vue'
import type { SupportTicketDetail, SupportTicket } from '@/api/supportTickets'

const mocks = vi.hoisted(() => ({
  route: { params: { id: '7' } },
  auth: { user: { id: 1, role: 'admin' } },
  access: { level: 'manage' },
  app: { cachedPublicSettings: { support_ticket_enabled: true } },
  get: vi.fn(), reply: vi.fn(), close: vi.fn(), remove: vi.fn(), list: vi.fn(), summary: vi.fn(),
  push: vi.fn(), success: vi.fn(), error: vi.fn(), refresh: vi.fn(), badge: vi.fn()
}))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', async importOriginal => ({ ...(await importOriginal<typeof import('vue-router')>()), useRoute: () => mocks.route, useRouter: () => ({ push: mocks.push }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ ...mocks.app, showSuccess: mocks.success, showError: mocks.error }) }))
vi.mock('@/stores/supportTickets', () => ({ useSupportTicketStore: () => ({ refresh: mocks.refresh, setUserUnread: mocks.badge, setAdminPending: mocks.badge }) }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await import('vue')
  return { useAdminPermission: () => ({ canView: computed(() => mocks.access.level !== 'none'), canManage: computed(() => mocks.access.level === 'manage') }) }
})
vi.mock('@/api/supportTickets', async importOriginal => ({
  ...(await importOriginal<typeof import('@/api/supportTickets')>()),
  getMyTicket: mocks.get, getTicket: mocks.get, replyMyTicket: mocks.reply, replyTicket: mocks.reply,
  closeMyTicket: mocks.close, reopenMyTicket: mocks.close, setTicketStatus: mocks.close, deleteTicket: mocks.remove,
  listMyTickets: mocks.list, listTickets: mocks.list, getMySummary: mocks.summary, getAdminSummary: mocks.summary
}))

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: { props: ['data'], template: '<div><p v-for="row in data" :key="row.id" data-testid="row">{{ row.title }}</p></div>' },
  Select: { props: ['modelValue', 'options'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>' },
  RouterLink: { template: '<a><slot /></a>' }, Icon: true, Pagination: true, EmptyState: true, CreateTicketDialog: true,
  ConfirmDialog: { props: ['show'], emits: ['confirm'], template: '<button v-if="show" data-test="confirm" @click="$emit(\'confirm\')">confirm</button>' }
}
const detail = (id = 7, title = `ticket ${id}`): SupportTicketDetail => ({
  ticket: { id, title, status: 'pending', user_id: 4, created_at: '2026-10-29T10:00:00Z' } as SupportTicket,
  messages: [{ id: 1, author_role: 'user', body: title, created_at: '2026-10-29T10:00:00Z' }]
})
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: unknown) => void; const promise = new Promise<T>((ok, no) => { resolve = ok; reject = no }); return { promise, resolve, reject } }
const listData = (id: number) => ({ items: [detail(id).ticket], total: 1, page: 1, page_size: 20, pages: 1 })

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  mocks.route = reactive({ params: { id: '7' } })
  mocks.auth = reactive({ user: { id: 1, role: 'admin' } })
  mocks.access = reactive({ level: 'manage' })
  mocks.app = reactive({ cachedPublicSettings: { support_ticket_enabled: true } })
  mocks.get.mockResolvedValue(detail())
  mocks.reply.mockResolvedValue(detail(7, 'new reply'))
  mocks.close.mockResolvedValue(detail())
  mocks.list.mockResolvedValue(listData(7))
  mocks.summary.mockResolvedValue({ pending_count: 2, categories: [], unread_count: 2, open_count: 2, max_open: 5 })
})
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

describe.each([['user', UserDetail, UserList], ['admin', AdminDetail, AdminList]] as const)('%s ticket request lifecycle', (_role, DetailView, ListView) => {
  const open = () => mount(DetailView, { global: { stubs } })

  it('drops an old GET after switching from ticket A to B and clears the draft', async () => {
    const old = deferred<SupportTicketDetail>()
    mocks.get.mockReturnValueOnce(old.promise).mockResolvedValueOnce(detail(8))
    const wrapper = open()
    mocks.route.params.id = '8'
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-title"]').text()).toBe('ticket 8')
    old.resolve(detail(7))
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-title"]').text()).toBe('ticket 8')
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
  })

  it.each(['resolve', 'reject'] as const)('ignores a late mutation %s after switching tickets without clearing the new draft', async outcome => {
    const wrapper = open()
    await flushPromises()
    const pending = deferred<SupportTicketDetail>()
    mocks.reply.mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-testid="ticket-reply-body"]').setValue('draft for A')
    await wrapper.get('[data-testid="ticket-reply"]').trigger('submit')
    mocks.get.mockResolvedValueOnce(detail(8))
    mocks.route.params.id = '8'
    await flushPromises()
    expect((wrapper.get('[data-testid="ticket-reply-body"]').element as HTMLTextAreaElement).value).toBe('')
    await wrapper.get('[data-testid="ticket-reply-body"]').setValue('draft for B')
    if (outcome === 'resolve') pending.resolve(detail(7, 'late A reply'))
    else pending.reject(new Error('late A failure'))
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-title"]').text()).toBe('ticket 8')
    expect((wrapper.get('[data-testid="ticket-reply-body"]').element as HTMLTextAreaElement).value).toBe('draft for B')
    expect(mocks.success).not.toHaveBeenCalled()
    expect(mocks.error).not.toHaveBeenCalled()
  })

  it('invalidates an in-flight poll before replying and skips new polls during the mutation', async () => {
    const wrapper = open()
    await flushPromises()
    const oldPoll = deferred<SupportTicketDetail>(), reply = deferred<SupportTicketDetail>()
    mocks.get.mockReturnValueOnce(oldPoll.promise)
    await vi.advanceTimersByTimeAsync(30_000)
    mocks.reply.mockReturnValueOnce(reply.promise)
    await wrapper.get('[data-testid="ticket-reply-body"]').setValue('new reply')
    await wrapper.get('[data-testid="ticket-reply"]').trigger('submit')
    await vi.advanceTimersByTimeAsync(30_000)
    expect(mocks.get).toHaveBeenCalledTimes(2)
    reply.resolve(detail(7, 'fresh reply'))
    await flushPromises()
    oldPoll.resolve(detail(7, 'old poll'))
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-title"]').text()).toBe('fresh reply')
    expect(mocks.success).toHaveBeenCalledTimes(1)
  })

  it('ignores pending reads on unmount and stops the poll timer', async () => {
    const pending = deferred<SupportTicketDetail>()
    mocks.get.mockReturnValueOnce(pending.promise)
    const wrapper = open()
    wrapper.unmount()
    pending.resolve(detail())
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(mocks.refresh).not.toHaveBeenCalled()
    expect(mocks.get).toHaveBeenCalledTimes(1)
  })

  it('ignores pending mutations on unmount without notifications or badge writes', async () => {
    const wrapper = open()
    await flushPromises()
    mocks.refresh.mockClear()
    const pending = deferred<SupportTicketDetail>()
    mocks.reply.mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-testid="ticket-reply-body"]').setValue('reply')
    await wrapper.get('[data-testid="ticket-reply"]').trigger('submit')
    wrapper.unmount()
    pending.resolve(detail())
    await flushPromises()
    expect(mocks.success).not.toHaveBeenCalled()
    expect(mocks.refresh).not.toHaveBeenCalled()
  })

  it('clears detail and drafts on a session change and drops the old reply', async () => {
    const wrapper = open()
    await flushPromises()
    const pending = deferred<SupportTicketDetail>()
    mocks.reply.mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-testid="ticket-reply-body"]').setValue('private draft')
    await wrapper.get('[data-testid="ticket-reply"]').trigger('submit')
    mocks.auth.user.id = 2
    await flushPromises()
    pending.resolve(detail())
    await flushPromises()
    expect(wrapper.find('[data-testid="ticket-title"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('private draft')
    expect(mocks.success).not.toHaveBeenCalled()
  })

  it('applies only the newest filter list response', async () => {
    const old = deferred<ReturnType<typeof listData>>()
    mocks.list.mockReturnValueOnce(old.promise).mockResolvedValueOnce(listData(8))
    const wrapper = mount(ListView, { global: { stubs } })
    if (_role === 'user') await wrapper.findAll('[data-testid="ticket-filter"]')[2].trigger('click')
    else await wrapper.get('[data-testid="ticket-status-filter"]').setValue('closed')
    await flushPromises()
    expect(wrapper.text()).toContain('ticket 8')
    old.resolve(listData(7))
    await flushPromises()
    expect(wrapper.text()).toContain('ticket 8')
    expect(wrapper.text()).not.toContain('ticket 7')
    expect(mocks.badge).toHaveBeenCalledTimes(1)
  })

  it('never updates badges from an unmounted list request', async () => {
    const pending = deferred<ReturnType<typeof listData>>()
    mocks.list.mockReturnValueOnce(pending.promise)
    const wrapper = mount(ListView, { global: { stubs } })
    wrapper.unmount()
    pending.resolve(listData(7))
    await flushPromises()
    expect(mocks.badge).not.toHaveBeenCalled()
    expect(mocks.error).not.toHaveBeenCalled()
  })
})

describe('ticket management permissions', () => {
  it('keeps view-only detail readable and hides all mutations', async () => {
    mocks.access.level = 'view'
    mocks.get.mockResolvedValueOnce({ ...detail(), ticket: { ...detail().ticket, user: { id: 4, email: 'operator-view@example.test', username: 'member', status: 'active', deleted: false } } })
    const wrapper = mount(AdminDetail, { global: { stubs } })
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-title"]').text()).toBe('ticket 7')
    expect(wrapper.text()).toContain('operator-view@example.test')
    expect(wrapper.text()).not.toContain('supportTickets.admin.balance')
    expect(wrapper.text()).not.toContain('supportTickets.admin.registeredAt')
    for (const id of ['ticket-reply', 'ticket-mark-processing', 'ticket-close', 'ticket-reopen', 'ticket-delete']) expect(wrapper.find(`[data-testid="${id}"]`).exists()).toBe(false)
    expect(mocks.reply).not.toHaveBeenCalled()
    expect(mocks.close).not.toHaveBeenCalled()
    expect(mocks.remove).not.toHaveBeenCalled()
  })

  it('fails closed without view access even when mounted directly', async () => {
    mocks.access.level = 'none'
    const wrapper = mount(AdminDetail, { global: { stubs } })
    await flushPromises()
    expect(mocks.get).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="ticket-title"]').exists()).toBe(false)
  })

  it('drops a mutation when manage permission is removed during the request', async () => {
    const wrapper = mount(AdminDetail, { global: { stubs } })
    await flushPromises()
    const pending = deferred<SupportTicketDetail>()
    mocks.reply.mockReturnValueOnce(pending.promise)
    await wrapper.get('[data-testid="ticket-reply-body"]').setValue('reply')
    await wrapper.get('[data-testid="ticket-reply"]').trigger('submit')
    mocks.access.level = 'view'
    await flushPromises()
    pending.resolve(detail(7, 'late secret'))
    await flushPromises()
    expect(wrapper.text()).not.toContain('late secret')
    expect(mocks.success).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="ticket-reply"]').exists()).toBe(false)
  })
})
