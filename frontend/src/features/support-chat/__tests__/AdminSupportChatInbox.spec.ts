import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, reactive } from 'vue'
import type { SupportChatAdminItem, SupportChatMessage, SupportChatThreadPage } from '@/api/supportChat'
import AdminSupportChatInbox from '../AdminSupportChatInbox.vue'
import SupportChatComposer from '../SupportChatComposer.vue'

const mocks = vi.hoisted(() => ({
  listAdminChats: vi.fn(), getAdminChat: vi.fn(), listAdminChatMessages: vi.fn(), markAdminChatRead: vi.fn(), sendAdminChatMessage: vi.fn(),
  setAdminChatStatus: vi.fn(), setAdminChatBlocked: vi.fn(), deleteAdminChatMessage: vi.fn(), deleteAdminChat: vi.fn(), fetchAdminChatImage: vi.fn(),
  success: vi.fn(), error: vi.fn(), warning: vi.fn(), replace: vi.fn(),
  manage: true,
  route: null as null | { query: Record<string, string | undefined> },
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
}))
vi.mock('vue-router', async () => ({
  ...(await vi.importActual<typeof import('vue-router')>('vue-router')),
  useRoute: () => mocks.route,
  useRouter: () => ({ replace: mocks.replace }),
}))
vi.mock('@/api/supportChat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportChat')>()),
  listAdminChats: mocks.listAdminChats, getAdminChat: mocks.getAdminChat, listAdminChatMessages: mocks.listAdminChatMessages,
  markAdminChatRead: mocks.markAdminChatRead, sendAdminChatMessage: mocks.sendAdminChatMessage, setAdminChatStatus: mocks.setAdminChatStatus,
  setAdminChatBlocked: mocks.setAdminChatBlocked, deleteAdminChatMessage: mocks.deleteAdminChatMessage, deleteAdminChat: mocks.deleteAdminChat,
  fetchAdminChatImage: mocks.fetchAdminChatImage,
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: mocks.success, showError: mocks.error, showWarning: mocks.warning, cachedPublicSettings: { support_ticket_enabled: true } }),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 1, role: 'admin' }, isAuthenticated: true }) }))
vi.mock('@/composables/useAdminPermission', () => ({
  useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => mocks.manage), has: () => true, isSuperAdmin: computed(() => true) }),
}))

const user = { id: 42, email: 'alice@example.com', username: 'alice', status: 'active', deleted: false, balance: 3, created_at: '2026-01-01T00:00:00Z' }
const chat = (id: number, extra: Partial<SupportChatAdminItem> = {}): SupportChatAdminItem => ({
  id, user_id: 42, status: 'open', waiting_since: new Date(Date.now() - 7 * 60_000).toISOString(), last_message_at: '2026-10-31T00:00:00Z',
  last_message_role: 'user', last_message_id: 3, preview: 'help please', preview_kind: 'text', unread: true, message_count: 3, blocked: false,
  blocked_reason: '', resolved_at: null, revision: 1, content_version: 1, read_message_id: 0, peer_read_message_id: 0, user, ...extra,
})
const message = (id: number, extra: Partial<SupportChatMessage> = {}): SupportChatMessage => ({
  id, author_role: 'user', body: `m${id}`, image: null, deleted: false, created_at: '2026-10-31T00:00:00Z', ...extra,
})
const page = (items: SupportChatMessage[], extra: Partial<SupportChatThreadPage> = {}): SupportChatThreadPage => ({
  items, has_more: false, status: 'open', blocked: false, read_message_id: 0, peer_read_message_id: 0, content_version: 1, unread_count: 0, ...extra,
})
const listOf = (...items: SupportChatAdminItem[]) => ({ items, total: items.length, page: 1, page_size: 20, pages: 1 })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}
async function summaryStore() {
  const { useSupportChatStore } = await import('@/stores/supportChat')
  const store = useSupportChatStore()
  const bump = async (version: number) => {
    store.setAdminSummary({ chat_waiting: 1, ticket_pending: 0, latest_user_message_id: 3, version })
    await flushPromises()
  }
  return { store, bump }
}

const stubs = {
  Icon: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
  ConfirmDialog: {
    props: ['show', 'message'],
    emits: ['confirm', 'cancel'],
    template: '<div v-if="show" data-testid="confirm"><span>{{ message }}</span><button type="button" data-testid="confirm-yes" @click="$emit(\'confirm\')">ok</button></div>',
  },
}

beforeEach(() => {
  vi.resetAllMocks()
  mocks.manage = true
  mocks.route = reactive({ query: { tab: 'chats' } as Record<string, string | undefined> })
  mocks.replace.mockImplementation(async ({ query }: { query: Record<string, string | undefined> }) => { mocks.route!.query = query })
  mocks.listAdminChats.mockResolvedValue({ items: [chat(7), chat(8, { waiting_since: null, preview_kind: 'image', preview: '', unread: false })], total: 2, page: 1, page_size: 20, pages: 1 })
  mocks.getAdminChat.mockResolvedValue(chat(7))
  mocks.listAdminChatMessages.mockResolvedValue(page([message(1), message(2, { author_role: 'admin', author_name: 'root' }), message(3)]))
  mocks.markAdminChatRead.mockResolvedValue({ read_message_id: 3 })
})

describe('admin chat inbox', () => {
  it('lists waiting chats oldest first and opens one with the user card', async () => {
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    expect(mocks.listAdminChats).toHaveBeenCalledWith({ filter: 'waiting', keyword: undefined, page: 1, page_size: 20 })
    const rows = wrapper.findAll('[data-testid="chat-row"]')
    expect(rows[0].get('[data-testid="chat-row-waiting"]').text()).toBe('supportChat.admin.waitingFor:{"minutes":7}')
    expect(rows[1].text()).toContain('supportChat.admin.previewImage')

    await rows[0].trigger('click')
    await flushPromises()
    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'chats', chat: '7' } })
    expect(mocks.listAdminChatMessages).toHaveBeenCalledWith(7, { limit: 30 })
    expect(mocks.getAdminChat).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-testid="ticket-user"]').text()).toContain('alice@example.com')
    expect(wrapper.get('[data-testid="support-user-usage"]').attributes('href')).toBe('/admin/usage?user_id=42')

    await wrapper.findAll('[data-testid="chat-filter"]')[2].trigger('click')
    await flushPromises()
    expect(mocks.listAdminChats).toHaveBeenLastCalledWith(expect.objectContaining({ filter: 'resolved', page: 1 }))
  })

  it('resolves with the poll cursor as last seen message and refetches on SUPPORT_CHAT_CHANGED', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    mocks.setAdminChatStatus.mockRejectedValueOnce({ status: 409, reason: 'SUPPORT_CHAT_CHANGED' })
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    await wrapper.get('[data-testid="chat-resolve"]').trigger('click')
    await flushPromises()
    expect(mocks.setAdminChatStatus).toHaveBeenCalledWith(7, 'resolved', 3)
    expect(mocks.warning).toHaveBeenCalledWith('supportChat.admin.changed')
    expect(mocks.listAdminChatMessages).toHaveBeenLastCalledWith(7, { after_id: 3, limit: 50 })
    expect(mocks.getAdminChat).toHaveBeenCalledTimes(2)

    mocks.setAdminChatStatus.mockResolvedValueOnce(chat(7, { status: 'resolved', waiting_since: null }))
    await wrapper.get('[data-testid="chat-resolve"]').trigger('click')
    await flushPromises()
    expect(mocks.success).toHaveBeenCalledWith('supportChat.admin.resolved')
    expect(wrapper.find('[data-testid="chat-reopen"]').exists()).toBe(true)
  })

  it('blocks as a two-state button and deletes a message after confirming', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    mocks.setAdminChatBlocked.mockResolvedValueOnce(chat(7, { blocked: true }))
    mocks.deleteAdminChatMessage.mockResolvedValueOnce({ message: message(1, { deleted: true, body: '' }) })
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    const block = wrapper.get('[data-testid="chat-block"]')
    expect(block.text()).toBe('supportChat.admin.block')
    await block.trigger('click')
    await flushPromises()
    expect(mocks.setAdminChatBlocked).toHaveBeenCalledWith(7, true)
    expect(wrapper.get('[data-testid="chat-block"]').text()).toBe('supportChat.admin.unblock')

    await wrapper.findAll('[data-testid="chat-message-delete"]')[0].trigger('click')
    expect(wrapper.get('[data-testid="confirm"]').text()).toContain('supportChat.admin.deleteMessageConfirm')
    await wrapper.get('[data-testid="confirm-yes"]').trigger('click')
    await flushPromises()
    expect(mocks.deleteAdminChatMessage).toHaveBeenCalledWith(7, 1)
    expect(wrapper.find('[data-testid="chat-message-deleted"]').exists()).toBe(true)
  })

  it('hides every manage control from view-only operators', async () => {
    mocks.manage = false
    mocks.route!.query = { tab: 'chats', chat: '7' }
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    expect(wrapper.findAll('[data-testid="chat-message"]')).toHaveLength(3)
    for (const id of ['chat-composer', 'chat-resolve', 'chat-block', 'chat-delete', 'chat-message-delete']) {
      expect(wrapper.find(`[data-testid="${id}"]`).exists(), id).toBe(false)
    }
  })

  it('resolves with the newest fetched id when the first page holds only staff messages', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    const staff = Array.from({ length: 30 }, (_, index) => message(100 + index, { author_role: 'admin', author_name: 'root' }))
    mocks.listAdminChatMessages.mockResolvedValue(page(staff, { has_more: true }))
    mocks.setAdminChatStatus.mockResolvedValueOnce(chat(7, { status: 'resolved', waiting_since: null }))
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    await wrapper.get('[data-testid="chat-resolve"]').trigger('click')
    await flushPromises()
    expect(mocks.setAdminChatStatus).toHaveBeenCalledWith(7, 'resolved', 129)
  })

  it('does not count its own newer reply as seeing a user message that was not polled yet', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    mocks.listAdminChatMessages.mockResolvedValue(page([message(100), message(101, { author_role: 'admin', author_name: 'root' })]))
    // User message 105 is committed on the server but not polled; our reply gets 106.
    mocks.sendAdminChatMessage.mockResolvedValueOnce({ message: message(106, { author_role: 'admin', author_name: 'root' }), duplicate: false })
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    wrapper.findComponent(SupportChatComposer).vm.$emit('send', { body: 'on it', image: null })
    await flushPromises()
    expect(wrapper.findAll('[data-testid="chat-message"]')).toHaveLength(3)
    await wrapper.get('[data-testid="chat-resolve"]').trigger('click')
    await flushPromises()
    expect(mocks.setAdminChatStatus).toHaveBeenCalledWith(7, 'resolved', 101)
  })

  it('keeps the resolve result when a version-triggered list read was in flight', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    const { bump } = await summaryStore()
    await bump(5)
    const post = deferred<SupportChatAdminItem>()
    mocks.setAdminChatStatus.mockReturnValueOnce(post.promise)
    await wrapper.get('[data-testid="chat-resolve"]').trigger('click')
    const before = deferred<ReturnType<typeof listOf>>()
    const after = deferred<ReturnType<typeof listOf>>()
    mocks.listAdminChats.mockReturnValueOnce(before.promise).mockReturnValueOnce(after.promise)
    await bump(6) // list read L1 starts
    post.resolve(chat(7, { status: 'resolved', waiting_since: null }))
    await flushPromises() // act() starts L2, which supersedes L1
    before.resolve(listOf(chat(7))) // pre-action row
    await flushPromises()
    after.resolve(listOf(chat(7, { status: 'resolved', waiting_since: null })))
    await flushPromises()
    expect(mocks.success).toHaveBeenCalledWith('supportChat.admin.resolved')
    expect(wrapper.find('[data-testid="chat-reopen"]').exists()).toBe(true)
  })

  it('keeps the block result when a version-triggered list read was in flight', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    const { bump } = await summaryStore()
    await bump(5)
    const before = deferred<ReturnType<typeof listOf>>()
    mocks.listAdminChats.mockReturnValueOnce(before.promise)
    await bump(6) // another chat changed: L1 starts
    mocks.setAdminChatBlocked.mockResolvedValueOnce(chat(7, { blocked: true }))
    const after = deferred<ReturnType<typeof listOf>>()
    mocks.listAdminChats.mockReturnValueOnce(after.promise)
    await wrapper.get('[data-testid="chat-block"]').trigger('click')
    await flushPromises() // block done, L2 in flight
    before.resolve(listOf(chat(7))) // superseded L1 with the pre-action row
    await flushPromises()
    expect(wrapper.get('[data-testid="chat-block"]').text()).toBe('supportChat.admin.unblock')
    after.resolve(listOf(chat(7, { blocked: true })))
    await flushPromises()
    expect(wrapper.get('[data-testid="chat-block"]').text()).toBe('supportChat.admin.unblock')
  })

  it('drops a detail read that was in flight when an action succeeded', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    mocks.listAdminChats.mockResolvedValue(listOf(chat(8))) // the open chat is not on this page
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    const { bump } = await summaryStore()
    await bump(5)
    const read = deferred<SupportChatAdminItem>()
    mocks.getAdminChat.mockReturnValueOnce(read.promise)
    await bump(6) // falls back to the audited detail read
    expect(mocks.getAdminChat).toHaveBeenCalledTimes(2)
    mocks.setAdminChatStatus.mockResolvedValueOnce(chat(7, { status: 'resolved', waiting_since: null }))
    await wrapper.get('[data-testid="chat-resolve"]').trigger('click')
    await flushPromises()
    read.resolve(chat(7))
    await flushPromises()
    expect(wrapper.find('[data-testid="chat-reopen"]').exists()).toBe(true)
  })

  it('reads the detail instead of copying a row from a list read a newer version read superseded', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    expect(mocks.getAdminChat).toHaveBeenCalledTimes(1)
    const { bump } = await summaryStore()
    await bump(5)
    const first = deferred<ReturnType<typeof listOf>>()
    const second = deferred<ReturnType<typeof listOf>>()
    mocks.listAdminChats.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    mocks.getAdminChat.mockResolvedValueOnce(chat(7, { status: 'resolved', waiting_since: null }))
    await bump(6) // L1 starts; `chats` still holds the open row from before
    await bump(7) // L2 supersedes L1 (same filter, so `chats` is not cleared)
    first.resolve(listOf(chat(7)))
    await flushPromises()
    expect(mocks.getAdminChat).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="chat-reopen"]').exists()).toBe(true)
    second.resolve(listOf(chat(7, { status: 'resolved', waiting_since: null })))
    await flushPromises()
    expect(wrapper.find('[data-testid="chat-reopen"]').exists()).toBe(true)
  })

  it('refetches the list and the open chat only when the summary version changes', async () => {
    mocks.route!.query = { tab: 'chats', chat: '7' }
    const wrapper = mount(AdminSupportChatInbox, { global: { stubs } })
    await flushPromises()
    const { useSupportChatStore } = await import('@/stores/supportChat')
    const store = useSupportChatStore()
    store.setAdminSummary({ chat_waiting: 1, ticket_pending: 0, latest_user_message_id: 3, version: 5 })
    await flushPromises()
    const listCalls = mocks.listAdminChats.mock.calls.length
    store.setAdminSummary({ chat_waiting: 1, ticket_pending: 0, latest_user_message_id: 3, version: 5 })
    await flushPromises()
    expect(mocks.listAdminChats).toHaveBeenCalledTimes(listCalls)
    store.setAdminSummary({ chat_waiting: 2, ticket_pending: 0, latest_user_message_id: 9, version: 6 })
    await flushPromises()
    expect(mocks.listAdminChats).toHaveBeenCalledTimes(listCalls + 1)
    expect(mocks.listAdminChatMessages).toHaveBeenLastCalledWith(7, { after_id: 3, limit: 50 })
    wrapper.unmount()
  })
})
