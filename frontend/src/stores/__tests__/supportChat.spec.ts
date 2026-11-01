import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SupportChatMessage, SupportChatThreadPage, SupportChatUserChat } from '@/api/supportChat'
import { IDLE_IMAGE_LIMIT, useSupportChatStore } from '../supportChat'
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
const chat = (id: number, extra: Partial<SupportChatUserChat> = {}): SupportChatUserChat => ({
  id, status: 'open', blocked: false, waiting: false, last_message_id: 0, read_message_id: 0, peer_read_message_id: 0, content_version: 0, ...extra,
})
const flushThread = () => new Promise((resolve) => setTimeout(resolve, 0))

let urlCounter = 0
beforeEach(() => {
  vi.resetAllMocks()
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

  it('reloads the loaded messages from the newest end when content_version changes', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(4), message(5), message(6)]))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { content_version: 2 }))
      .mockResolvedValueOnce(page([message(4), message(5, { deleted: true, body: '' }), message(6)], { content_version: 2 }))
    await thread.poll()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 50 })
    expect(thread.state.messages[1]).toMatchObject({ id: 5, deleted: true })
    expect(thread.state.contentVersion).toBe(2)
    expect(thread.cursor()).toBe(6)
  })

  it('keeps the newest messages and a matching cursor when reloading more than 1000 messages', async () => {
    // Server: ids 5001..6200; only messages before 6201 exist until the end of the test.
    let all = Array.from({ length: 1200 }, (_, index) => message(5001 + index, { author_role: index % 2 ? 'user' : 'admin' }))
    const serve = (query: { after_id?: number; before_id?: number; limit?: number }) => {
      const limit = Math.min(query.limit ?? 30, 50)
      if (query.after_id) {
        const newer = all.filter((item) => item.id > query.after_id!)
        return page(newer.slice(0, limit), { has_more: newer.length > limit, content_version: version })
      }
      const older = query.before_id ? all.filter((item) => item.id < query.before_id!) : all
      return page(older.slice(-limit), { has_more: older.length > limit, content_version: version })
    }
    let version = 1
    mocks.listMyChatMessages.mockImplementation(async (query) => serve(query))
    const thread = useSupportChatStore().userThread()
    await thread.load()
    while (thread.state.hasOlder) await thread.loadOlder()
    expect(thread.state.messages).toHaveLength(1200)
    expect(thread.cursor()).toBe(6200)

    version = 2
    all = all.map((item) => (item.id === 5100 ? { ...item, deleted: true, body: '' } : item))
    await thread.poll()
    const loaded = ids(thread.state.messages)
    expect(loaded).toHaveLength(1000)
    expect(loaded[loaded.length - 1]).toBe(6200)
    expect(loaded[0]).toBe(5201)
    expect(thread.cursor()).toBe(thread.maxLoadedId())
    expect(thread.state.hasOlder).toBe(true)
    expect(thread.state.contentVersion).toBe(2)

    all = [...all, message(6201)]
    await thread.poll()
    expect(ids(thread.state.messages).slice(-3)).toEqual([6199, 6200, 6201])
    // Scrolling up brings the dropped older range back.
    await thread.loadOlder()
    expect(thread.state.messages[0].id).toBe(5171)
  })

  it('reloads only up to the previously loaded count, with the cursor at the newest message read', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(50), message(51)], { has_more: true }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([message(52)], { content_version: 2 }))
      .mockResolvedValueOnce(page([message(49), message(50), message(51, { deleted: true, body: '' }), message(52)], { content_version: 2, has_more: true }))
    await thread.poll()
    expect(mocks.listMyChatMessages).toHaveBeenCalledTimes(3) // the latest page reaches 50: no older page
    // 49 is older than the previous window: not prepended above the reader.
    expect(ids(thread.state.messages)).toEqual([50, 51, 52])
    expect(thread.cursor()).toBe(52)
    expect(thread.state.hasOlder).toBe(true)
  })

  it('keeps the top of a 30-message window when the reload reads a full page of 50', async () => {
    const thread = useSupportChatStore().userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, index) => message(from + index))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { has_more: true }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { content_version: 2 }))
      .mockResolvedValueOnce(page(range(51, 100).map((item) => (item.id === 90 ? { ...item, deleted: true, body: '' } : item)), { content_version: 2, has_more: true }))
    await thread.poll()
    expect(mocks.listMyChatMessages).toHaveBeenCalledTimes(3)
    const loaded = ids(thread.state.messages)
    expect(loaded).toEqual(ids(range(71, 100)))
    expect(thread.state.messages.find((item) => item.id === 90)).toMatchObject({ deleted: true })
    expect(thread.state.hasOlder).toBe(true)
    expect(thread.cursor()).toBe(100)
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

  it('shares one objectURL per image, keeps it after the last release and revokes it on reset', async () => {
    mocks.fetchMyChatImage.mockResolvedValue(new Blob(['a'], { type: 'image/webp' }))
    mocks.fetchAdminChatImage.mockResolvedValue(new Blob(['b'], { type: 'image/png' }))
    const store = useSupportChatStore()
    const [first, second] = await Promise.all([store.acquireImage('user', 1), store.acquireImage('user', 1)])
    expect(first).toBe(second)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(1)
    store.releaseImage('user', 1)
    store.releaseImage('user', 1)
    store.releaseImage('user', 1) // a stray extra release changes nothing
    // Images are no-store on the server: the idle URL is reused, not downloaded again.
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    await expect(store.acquireImage('user', 1)).resolves.toBe(first)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(1)

    const admin = await store.acquireImage('admin', 1)
    expect(mocks.fetchAdminChatImage).toHaveBeenCalledWith(1)
    store.reset()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(first)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(admin)
  })

  it('keeps at most IDLE_IMAGE_LIMIT released objectURLs and revokes the least recently released', async () => {
    mocks.fetchMyChatImage.mockImplementation(async (id: number) => new Blob([String(id)]))
    const store = useSupportChatStore()
    const urls = new Map<number, string>()
    for (let id = 1; id <= IDLE_IMAGE_LIMIT + 1; id++) urls.set(id, await store.acquireImage('user', id))
    store.releaseImage('user', 2)
    store.releaseImage('user', 1)
    for (let id = 3; id <= IDLE_IMAGE_LIMIT; id++) store.releaseImage('user', id)
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    store.releaseImage('user', IDLE_IMAGE_LIMIT + 1) // one past the limit: 2 was released first
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith(urls.get(2))
    // Re-acquiring an idle image takes it out of the LRU; an evicted one is fetched again.
    await store.acquireImage('user', 1)
    await store.acquireImage('user', 2)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(IDLE_IMAGE_LIMIT + 2)
    store.reset()
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(IDLE_IMAGE_LIMIT + 2)
  })

  it('parks an image released before its download finished', async () => {
    let resolve!: (blob: Blob) => void
    mocks.fetchMyChatImage.mockReturnValueOnce(new Promise((r) => { resolve = r }))
    const store = useSupportChatStore()
    const pending = store.acquireImage('user', 9)
    store.releaseImage('user', 9)
    resolve(new Blob(['x']))
    const url = await pending
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    await expect(store.acquireImage('user', 9)).resolves.toBe(url)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(1)
  })

  it('clears the loaded conversation when staff deleted it (summary chat:null, poll no-chat page)', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(7, { last_message_id: 2 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0 }))
    await thread.load()
    await store.refreshSummary()
    expect(mocks.listMyChatMessages).toHaveBeenCalledTimes(1)

    // Unblocked delete: no row any more.
    mocks.getMyChat.mockResolvedValueOnce({ chat: null, unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
    await store.refreshSummary()
    await flushThread()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(thread.state.messages).toEqual([])
    expect(thread.cursor()).toBe(0)

    // The same through the conversation's own poll.
    store.reset()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0 }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
      .mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
    await thread.poll()
    expect(thread.state.messages).toEqual([])
    expect(thread.cursor()).toBe(0)
    // A later first message of a new chat appears alone, not under the deleted history.
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(10, { author_role: 'user' })]))
    await thread.poll()
    expect(ids(thread.state.messages)).toEqual([10])
  })

  it('replaces the conversation when the summary shows a new chat id (blocked user, chat deleted)', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0, read_message_id: 2 }))
    await thread.load()
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(7, { last_message_id: 2, read_message_id: 2 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await store.refreshSummary()

    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(9, { status: 'resolved', blocked: true }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0 }))
    await store.refreshSummary()
    await flushThread()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(thread.state.messages).toEqual([])
    expect(thread.state.blocked).toBe(true)
    expect(thread.cursor()).toBe(0)

    // A further summary for the same new chat changes nothing.
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(9, { status: 'resolved', blocked: true }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await store.refreshSummary()
    await flushThread()
    expect(mocks.listMyChatMessages).toHaveBeenCalledTimes(2)
  })

  it('replaces the conversation when the summary shows a new chat id with newer messages', async () => {
    // Unblocked chat 7 deleted; the user wrote again before a poll saw the no-chat page.
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0 }))
    await thread.load()
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(7, { last_message_id: 2 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await store.refreshSummary()
    expect(mocks.listMyChatMessages).toHaveBeenCalledTimes(1)

    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(9, { last_message_id: 10 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValue(page([message(10, { author_role: 'user' })], { content_version: 0 }))
    await store.refreshSummary()
    await flushThread()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(ids(thread.state.messages)).toEqual([10])
  })

  it('does not treat a summary older than a concurrent poll as a deleted chat', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => message(from + i))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { content_version: 0, has_more: true }))
    await thread.load()
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(41, 70), { content_version: 0 }))
    await thread.loadOlder()
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(7, { last_message_id: 100 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await store.refreshSummary()

    // The summary is read before message 101 commits but answers after a poll fetched it.
    let answer!: (value: unknown) => void
    mocks.getMyChat.mockReturnValueOnce(new Promise((resolve) => { answer = resolve }))
    const summary = store.refreshSummary()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(101)], { content_version: 0 }))
    await thread.poll()
    expect(thread.cursor()).toBe(101)
    answer({ chat: chat(7, { last_message_id: 100 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await summary
    await flushThread()
    expect(mocks.listMyChatMessages).not.toHaveBeenLastCalledWith({ limit: 30 })
    expect(ids(thread.state.messages)).toEqual(ids(range(41, 101)))
  })

  it('remembers the chat from a summary that arrived before the thread loaded', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(7, { last_message_id: 2 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await store.refreshSummary()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0 }))
    await thread.load()

    // Chat 7 deleted, the user wrote again: chat 9 with a newer id and no cursor going backwards.
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(9, { last_message_id: 10 }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValue(page([message(10, { author_role: 'user' })], { content_version: 0 }))
    await store.refreshSummary()
    await flushThread()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(ids(thread.state.messages)).toEqual([10])
  })

  it('clears a thread that holds only the user\'s own sends when the chat is deleted', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
    await thread.load()
    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(5, { author_role: 'user', client_msg_id: 'a' }), duplicate: false })
    await thread.send('first')
    expect(ids(thread.state.messages)).toEqual([5])
    expect(thread.cursor()).toBe(0)

    // By summary.
    mocks.getMyChat.mockResolvedValueOnce({ chat: null, unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
    await store.refreshSummary()
    await flushThread()
    expect(thread.state.messages).toEqual([])

    // By the conversation's own poll.
    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(6, { author_role: 'user', client_msg_id: 'b' }), duplicate: false })
    await thread.send('again')
    expect(ids(thread.state.messages)).toEqual([6])
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
      .mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
    await thread.poll()
    await flushThread()
    expect(thread.state.messages).toEqual([])
  })

  it('clears a first conversation that staff deleted and blocked before any poll (chat ids differ)', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.getMyChat.mockResolvedValueOnce({ chat: null, unread_count: 0, ticket_unread_count: 0, welcome: '' })
    await store.refreshSummary()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: '' as never, content_version: 0, chat_id: 0 }))
    await thread.load()
    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(5, { author_role: 'user', client_msg_id: 'a' }), duplicate: false, chat_id: 7 })
    await thread.send('first')
    expect(thread.chatId()).toBe(7)

    // Chat 7 deleted while blocked: an empty placeholder 9 with no cursors to compare.
    mocks.getMyChat.mockResolvedValueOnce({ chat: chat(9, { status: 'resolved', blocked: true }), unread_count: 0, ticket_unread_count: 0, welcome: '' })
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
    await store.refreshSummary()
    await flushThread()
    expect(thread.state.messages).toEqual([])
    expect(thread.state.blocked).toBe(true)
    expect(thread.chatId()).toBe(9)
  })

  it('reloads when its own poll returns a page for another chat', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0, chat_id: 7 }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
    await thread.poll()
    expect(thread.state.messages).toEqual([])
    expect(thread.chatId()).toBe(9)
  })

  it('reloads when a send lands in another chat', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0, chat_id: 7 }))
    await thread.load()
    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(20, { author_role: 'user', client_msg_id: 'b' }), duplicate: false, chat_id: 9 })
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(20, { author_role: 'user' })], { content_version: 0, chat_id: 9 }))
    await thread.send('hello again')
    await flushThread()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(ids(thread.state.messages)).toEqual([20])
  })

  it('learns the chat id from a poll and resets when a later poll shows another chat', async () => {
    // The chat was created in another tab, so no send result told this thread its id.
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: '' as never, content_version: 0, chat_id: 0 }))
    await thread.load()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(3, { author_role: 'user' })], { content_version: 0, chat_id: 7 }))
    await thread.poll()
    expect(thread.chatId()).toBe(7)
    expect(ids(thread.state.messages)).toEqual([3])
    // Deleted while blocked: an empty placeholder with no cursors to compare.
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
    await thread.poll()
    expect(thread.state.messages).toEqual([])
    expect(thread.chatId()).toBe(9)
  })

  it('keeps the old chat id when the reload after a replacing send fails, so the next poll recovers', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0, chat_id: 7 }))
    await thread.load()
    mocks.sendMyChatMessage.mockResolvedValueOnce({ message: message(20, { author_role: 'user', client_msg_id: 'c' }), duplicate: false, chat_id: 9 })
    mocks.listMyChatMessages.mockRejectedValueOnce(new Error('network'))
    await thread.send('again')
    await flushThread()
    expect(thread.chatId()).toBe(7)
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([message(21)], { content_version: 0, chat_id: 9 }))
      .mockResolvedValueOnce(page([message(20, { author_role: 'user' }), message(21)], { content_version: 0, chat_id: 9 }))
    await thread.poll()
    expect(ids(thread.state.messages)).toEqual([20, 21])
    expect(thread.chatId()).toBe(9)
  })

  it('drops an older-page read that was in flight when the chat was replaced', async () => {
    const thread = useSupportChatStore().userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => message(from + i))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { content_version: 0, has_more: true, chat_id: 7 }))
    await thread.load()
    let answerOlder!: (value: SupportChatThreadPage) => void
    mocks.listMyChatMessages.mockReturnValueOnce(new Promise((resolve) => { answerOlder = resolve }))
    const older = thread.loadOlder()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
      .mockResolvedValueOnce(page([message(5, { author_role: 'user' })], { status: 'resolved', blocked: true, content_version: 0, chat_id: 9 }))
    await thread.poll()
    expect(ids(thread.state.messages)).toEqual([5])
    answerOlder(page(range(41, 70), { content_version: 0, chat_id: 7 }))
    await older
    expect(ids(thread.state.messages)).toEqual([5])
    expect(thread.state.loadingOlder).toBe(false)
  })

  it('starts over when a message-delete reload reads pages from a replaced chat', async () => {
    const thread = useSupportChatStore().userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => message(from + i))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { content_version: 0, has_more: true, chat_id: 7 }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { content_version: 1, chat_id: 7 }))
      .mockResolvedValueOnce(page(range(81, 100), { content_version: 1, has_more: true, chat_id: 7 }))
      .mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0, chat_id: 9 }))
      .mockResolvedValueOnce(page([message(5, { author_role: 'user' })], { content_version: 0, chat_id: 9 }))
    await thread.poll()
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(ids(thread.state.messages)).toEqual([5])
    expect(thread.chatId()).toBe(9)
  })

  it('drops a late older page of a chat deleted outright (no-chat page)', async () => {
    const thread = useSupportChatStore().userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => message(from + i))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { content_version: 0, has_more: true, chat_id: 7 }))
    await thread.load()
    let answerOlder!: (value: SupportChatThreadPage) => void
    mocks.listMyChatMessages.mockReturnValueOnce(new Promise((resolve) => { answerOlder = resolve }))
    const older = thread.loadOlder()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: '' as never, content_version: 0, chat_id: 0 }))
      .mockResolvedValueOnce(page([], { status: '' as never, content_version: 0, chat_id: 0 }))
    await thread.poll()
    expect(thread.state.messages).toEqual([])
    expect(thread.chatId()).toBe(0)
    answerOlder(page(range(41, 70), { content_version: 0, chat_id: 7 }))
    await older
    expect(thread.state.messages).toEqual([])
  })

  it('drops an older page read before a same-chat message-delete reload', async () => {
    const thread = useSupportChatStore().userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => message(from + i))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { content_version: 0, has_more: true, chat_id: 7 }))
    await thread.load()
    let answerOlder!: (value: SupportChatThreadPage) => void
    mocks.listMyChatMessages.mockReturnValueOnce(new Promise((resolve) => { answerOlder = resolve }))
    const older = thread.loadOlder()
    // Staff deleted message 50: the poll sees content_version 1 and the window is re-read.
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { content_version: 1, chat_id: 7 }))
      .mockResolvedValueOnce(page(range(71, 100), { content_version: 1, has_more: true, chat_id: 7 }))
    await thread.poll()
    expect(thread.state.contentVersion).toBe(1)
    answerOlder(page(range(41, 70), { content_version: 0, has_more: true, chat_id: 7 }))
    await older
    expect(ids(thread.state.messages)).not.toContain(50)
    expect(ids(thread.state.messages)[0]).toBe(71)
  })

  it('starts over when the message-delete reload already reads the replacing chat', async () => {
    const thread = useSupportChatStore().userThread()
    const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => message(from + i))
    mocks.listMyChatMessages.mockResolvedValueOnce(page(range(71, 100), { content_version: 0, has_more: true, chat_id: 7, read_message_id: 100, peer_read_message_id: 100 }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { content_version: 1, chat_id: 7, read_message_id: 100, peer_read_message_id: 100 }))
      .mockResolvedValueOnce(page([message(201)], { content_version: 0, chat_id: 9 }))
      .mockResolvedValueOnce(page([message(201)], { content_version: 0, chat_id: 9 }))
    await thread.poll()
    expect(thread.chatId()).toBe(9)
    expect(mocks.listMyChatMessages).toHaveBeenLastCalledWith({ limit: 30 })
    expect(thread.state.readId).toBe(0)
  })

  it('detects a replaced chat on its own poll when the read cursors go backwards', async () => {
    const thread = useSupportChatStore().userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1), message(2)], { content_version: 0, read_message_id: 2, peer_read_message_id: 1 }))
    await thread.load()
    mocks.listMyChatMessages
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0 }))
      .mockResolvedValueOnce(page([], { status: 'resolved', blocked: true, content_version: 0 }))
    await thread.poll()
    expect(thread.state.messages).toEqual([])
    expect(thread.state.blocked).toBe(true)
    expect(thread.cursor()).toBe(0)
  })

  it('keeps a pending send across a conversation reload', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.listMyChatMessages.mockResolvedValueOnce(page([message(1)], { content_version: 0 }))
    await thread.load()
    mocks.sendMyChatMessage.mockRejectedValueOnce({ status: 0 })
    await thread.send('still here')
    mocks.listMyChatMessages.mockResolvedValueOnce(page([], { status: '' as never, content_version: 0 }))
    await thread.reload()
    expect(thread.state.messages).toEqual([])
    expect(thread.state.pending).toHaveLength(1)
  })

  it('sends no thread request once the user may not chat any more (logged out)', async () => {
    const store = useSupportChatStore()
    const thread = store.userThread()
    mocks.auth.isAuthenticated = false
    mocks.auth.user = null
    store.reset()
    await thread.poll()
    await thread.send('x')
    expect(mocks.listMyChatMessages).not.toHaveBeenCalled()
    expect(mocks.sendMyChatMessage).not.toHaveBeenCalled()
  })

  it('forgets failed image requests so a retry fetches again', async () => {
    mocks.fetchMyChatImage.mockRejectedValueOnce({ status: 500 }).mockResolvedValueOnce(new Blob(['a']))
    const store = useSupportChatStore()
    await expect(store.acquireImage('user', 2)).rejects.toEqual({ status: 500 })
    await expect(store.acquireImage('user', 2)).resolves.toMatch(/^blob:/)
    expect(mocks.fetchMyChatImage).toHaveBeenCalledTimes(2)
  })
})
