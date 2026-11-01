import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SupportChatMessage, SupportChatThreadPage } from '@/api/supportChat'
import { useSupportChatStore } from '../supportChat'
import { useSupportTicketStore } from '../supportTickets'

const mocks = vi.hoisted(() => ({
  getMyChat: vi.fn(), listMyChatMessages: vi.fn(), sendMyChatMessage: vi.fn(), markMyChatRead: vi.fn(),
  fetchMyChatImage: vi.fn(), fetchAdminChatImage: vi.fn(),
  auth: { isAuthenticated: true, canAccessAdmin: false, user: { id: 5 } as { id: number } | null },
  app: { cachedPublicSettings: { support_chat_enabled: true } as Record<string, unknown> | null },
}))
vi.mock('@/api/supportChat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportChat')>()),
  getMyChat: mocks.getMyChat, listMyChatMessages: mocks.listMyChatMessages, sendMyChatMessage: mocks.sendMyChatMessage,
  markMyChatRead: mocks.markMyChatRead, fetchMyChatImage: mocks.fetchMyChatImage, fetchAdminChatImage: mocks.fetchAdminChatImage,
}))
vi.mock('../auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('../app', () => ({ useAppStore: () => mocks.app }))

const message = (id: number, extra: Partial<SupportChatMessage> = {}): SupportChatMessage => ({
  id, author_role: 'admin', body: `m${id}`, image: null, deleted: false, created_at: '2026-10-31T00:00:00Z', ...extra,
})
const page = (items: SupportChatMessage[], extra: Partial<SupportChatThreadPage> = {}): SupportChatThreadPage => ({
  items, has_more: false, status: 'open', blocked: false, read_message_id: 0, peer_read_message_id: 0, content_version: 1, unread_count: 0, ...extra,
})
const ids = (items: SupportChatMessage[]) => items.map((item) => item.id)

let urlCounter = 0
beforeEach(() => {
  vi.clearAllMocks()
  urlCounter = 0
  mocks.auth.isAuthenticated = true
  mocks.auth.canAccessAdmin = false
  mocks.auth.user = { id: 5 }
  mocks.app.cachedPublicSettings = { support_chat_enabled: true }
  vi.stubGlobal('URL', Object.assign(URL, {
    createObjectURL: vi.fn(() => `blob:${++urlCounter}`),
    revokeObjectURL: vi.fn(),
  }))
})

afterEach(() => {
  vi.useRealTimers()
})

describe('support chat thread', () => {
  it('only moves the poll cursor on GET windows and after_id pages, following has_more', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2, { author_role: 'user' })], { has_more: true }))
    await thread.load()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(thread.cursor()).toBe(2)
    expect(thread.state.hasOlder).toBe(true)

    // Our own message gets id 5, but a staff message (id 4) committed before it.
    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(5, { author_role: 'user', client_msg_id: 'x' }), duplicate: false })
    await thread.send('hello')
    expect(ids(thread.state.messages)).toEqual([1, 2, 5])
    expect(thread.state.pending).toHaveLength(0)
    expect(thread.cursor()).toBe(2)

    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([message(3), message(4)], { has_more: true }))
      .mockResolvedValueOnce(page([message(5, { author_role: 'user' }), message(6)]))
    await thread.poll()
    expect(mocks.listMyChatMessages.mock.calls.slice(1).map(([query]) => query)).toEqual([
      { after_id: 2, limit: 50 },
      { after_id: 4, limit: 50 },
    ])
    expect(ids(thread.state.messages)).toEqual([1, 2, 3, 4, 5, 6])
    expect(thread.cursor()).toBe(6)
  })

  it('loads older pages with before_id without touching the cursor', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(10), message(11)], { has_more: true }))
    await thread.load()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(8), message(9)], { has_more: false }))
    await thread.loadOlder()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ before_id: 10, limit: 30 })
    expect(ids(thread.state.messages)).toEqual([8, 9, 10, 11])
    expect(thread.state.hasOlder).toBe(false)
    expect(thread.cursor()).toBe(11)
  })

  it('keeps a failed message for a manual resend with the same client_msg_id', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([]))
    await thread.load()
    mocks.sendMyChatMessage.mockRejectedValueOnce({ status: 0, message: 'offline' })
    expect(await thread.send('help')).toBe(false)
    expect(mocks.sendMyChatMessage).toHaveBeenCalledTimes(1) // no automatic retry
    const [pending] = thread.state.pending
    expect(pending.state).toBe('failed')
    const clientId = mocks.sendMyChatMessage.mock.calls[0][0].client_msg_id
    expect(pending.client_msg_id).toBe(clientId)

    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(1, { author_role: 'user', client_msg_id: clientId }), duplicate: true })
    expect(await thread.retry(clientId)).toBe(true)
    expect(mocks.sendMyChatMessage.mock.calls[1][0]).toMatchObject({ body: 'help', client_msg_id: clientId })
    expect(thread.state.pending).toHaveLength(0)
    expect(ids(thread.state.messages)).toEqual([1])
  })

  it('replaces a pending message when a poll brings it back first', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([]))
    await thread.load()
    let resolveSend!: (value: unknown) => void
    mocks.sendMyChatMessage.mockReturnValueOnce(new Promise((resolve) => { resolveSend = resolve }))
    const sending = thread.send('hi')
    const clientId = thread.state.pending[0].client_msg_id
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(3, { author_role: 'user', client_msg_id: clientId })]))
    await thread.poll()
    expect(thread.state.pending).toHaveLength(0)
    resolveSend({ message: message(3, { author_role: 'user', client_msg_id: clientId }), duplicate: false })
    await sending
    expect(ids(thread.state.messages)).toEqual([3])
  })

  it('reloads every loaded message when content_version changes', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(4), message(5), message(6)]))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { content_version: 2 }))
      .mockResolvedValueOnce(page([message(4), message(5, { deleted: true, body: '' }), message(6)], { content_version: 2 }))
    await thread.poll()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ after_id: 3, limit: 50 })
    expect(thread.state.messages[1]).toMatchObject({ id: 5, deleted: true })
    expect(thread.state.contentVersion).toBe(2)
  })

  it('drops responses that arrive after a reset', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    let resolveList!: (value: SupportChatThreadPage) => void
    mocks.listMyChatMessages.mockReturnValueOnce(new Promise((resolve) => { resolveList = resolve }))
    const loading = thread.load()
    store.reset()
    resolveList(page([message(1)]))
    await loading
    expect(thread.state.messages).toEqual([])
    expect(thread.state.loaded).toBe(false)
  })

  it('reports reads only while visible, debounced, and only past the read cursor', async () => {
    vi.useFakeTimers()
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { unread_count: 2, read_message_id: 0 }))
    await thread.load()
    expect(store.userChatUnread).toBe(2)
    await vi.advanceTimersByTimeAsync(2000)
    expect(mocks.markMyChatRead).not.toHaveBeenCalled()

    mocks.markMyChatRead.mockResolvedValue({ read_message_id: 2 })
    thread.setVisible(true)
    await vi.advanceTimersByTimeAsync(999)
    expect(mocks.markMyChatRead).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(mocks.markMyChatRead).toHaveBeenCalledWith(2)
    expect(store.userChatUnread).toBe(0)

    thread.scheduleRead()
    await vi.advanceTimersByTimeAsync(2000)
    expect(mocks.markMyChatRead).toHaveBeenCalledTimes(1)
    thread.setVisible(false)
  })
})

describe('support chat store', () => {
  it('reads the summary into both badges with one request', async () => {
    mocks.getMyChat.mockResolvedValue({ chat: null, unread_count: 3, ticket_unread_count: 2, welcome: 'hello' })
    const store = useSupportChatStore()
    await store.refreshSummary()
    expect(store.userChatUnread).toBe(3)
    expect(useSupportTicketStore().userUnread).toBe(2)
    expect(store.welcome).toBe('hello')
  })

  it('never calls the chat endpoints for guests, staff or with the flag off', async () => {
    const store = useSupportChatStore()
    mocks.auth.isAuthenticated = false
    await store.refreshSummary()
    mocks.auth.isAuthenticated = true
    mocks.auth.canAccessAdmin = true
    await store.refreshSummary()
    mocks.auth.canAccessAdmin = false
    mocks.app.cachedPublicSettings = { support_chat_enabled: false }
    await store.refreshSummary()
    mocks.app.cachedPublicSettings = {}
    await store.refreshSummary()
    expect(mocks.getMyChat).not.toHaveBeenCalled()
  })

  it('ignores a summary for a previous user', async () => {
    let resolve!: (value: unknown) => void
    mocks.getMyChat.mockReturnValueOnce(new Promise((r) => { resolve = r }))
    const store = useSupportChatStore()
    const pending = store.refreshSummary()
    mocks.auth.user = { id: 6 }
    resolve({ chat: null, unread_count: 9, ticket_unread_count: 9, welcome: '' })
    await pending
    expect(store.userChatUnread).toBe(0)
  })

  it('shares one objectURL per image and revokes it after the last release or a reset', async () => {
    mocks.fetchMyChatImage.mockResolvedValue(new Blob(['a'], { type: 'image/webp' }))
    mocks.fetchAdminChatImage.mockResolvedValue(new Blob(['b'], { type: 'image/png' }))
    const store = useSupportChatStore()
    const [first, second] = await Promise.all([store.acquireImage('user', 1), store.acquireImage('user', 1)])
    expect(first).toBe(second)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(1)
    store.releaseImage('user', 1)
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    store.releaseImage('user', 1)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(first)

    const admin = await store.acquireImage('admin', 1)
    expect(mocks.fetchAdminChatImage).toHaveBeenCalledWith(1)
    store.reset()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(admin)
  })

  it('forgets failed image requests so a retry fetches again', async () => {
    mocks.fetchMyChatImage.mockRejectedValueOnce({ status: 500 }).mockResolvedValueOnce(new Blob(['a']))
    const store = useSupportChatStore()
    await expect(store.acquireImage('user', 2)).rejects.toEqual({ status: 500 })
    await expect(store.acquireImage('user', 2)).resolves.toMatch(/^blob:/)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(2)
  })
})
