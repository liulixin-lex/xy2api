import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, defineComponent, h } from 'vue'
import type { SupportChatImageRef, SupportChatMessage, SupportChatThreadPage } from '@/api/supportChat'
import { useSupportChatStore } from '@/stores/supportChat'
import { CHAT_POLL_FAST_MS, createChatThread, useChatThreadPolling, type ChatThread } from '../chatThread'
import SupportChatThread from '../SupportChatThread.vue'
import SupportChatImage from '../SupportChatImage.vue'
import SupportChatComposer from '../SupportChatComposer.vue'
import SupportChatConversation from '../SupportChatConversation.vue'

const mocks = vi.hoisted(() => ({
  fetchMyChatImage: vi.fn(), fetchAdminChatImage: vi.fn(), compressImage: vi.fn(), error: vi.fn(),
  recallMyChatMessage: vi.fn(), listMyChatMessages: vi.fn(), getMyChat: vi.fn(), markMyChatRead: vi.fn(),
  auth: { isAuthenticated: true, canAccessAdmin: false, user: { id: 42 } },
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: mocks.error, showSuccess: vi.fn(), cachedPublicSettings: { support_ticket_enabled: true, support_chat_enabled: true } }),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/api/supportChat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportChat')>()),
  fetchMyChatImage: mocks.fetchMyChatImage, fetchAdminChatImage: mocks.fetchAdminChatImage,
  recallMyChatMessage: mocks.recallMyChatMessage, listMyChatMessages: mocks.listMyChatMessages, getMyChat: mocks.getMyChat, markMyChatRead: mocks.markMyChatRead,
}))
vi.mock('../compressImage', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../compressImage')>()),
  compressImage: mocks.compressImage,
}))

const DialogStub = { props: ['show', 'title', 'zIndex'], template: '<div v-if="show" data-testid="lightbox" :data-z="zIndex"><slot /></div>' }
const stubs = { BaseDialog: DialogStub, Icon: true }

const image = (extra: Partial<SupportChatImageRef> = {}): SupportChatImageRef => ({ id: 9, content_type: 'image/webp', width: 800, height: 600, byte_size: 1000, purged: false, ...extra })
const message = (id: number, extra: Partial<SupportChatMessage> = {}): SupportChatMessage => ({
  id, author_role: 'admin', body: `m${id}`, image: null, deleted: false, created_at: '2026-10-31T00:00:00Z', ...extra,
})
const page = (items: SupportChatMessage[], extra: Partial<SupportChatThreadPage> = {}): SupportChatThreadPage => ({
  items, has_more: false, status: 'open', blocked: false, read_message_id: 0, peer_read_message_id: 0, content_version: 1, unread_count: 0, ...extra,
})

beforeEach(() => {
  vi.clearAllMocks()
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:preview'), revokeObjectURL: vi.fn() })
})

describe('chat thread', () => {
  async function threadWith(items: SupportChatMessage[], extra: Partial<SupportChatThreadPage> = {}, send = vi.fn()) {
    const thread = createChatThread({ list: vi.fn().mockResolvedValue(page(items, extra)), send, markRead: vi.fn() }, { viewer: 'user' })
    await thread.load()
    return thread
  }

  it('names staff only by account id, words each removal and never shows users read state', async () => {
    const thread = await threadWith([
      message(1, { author_role: 'user' }),
      message(2, { author_name: 'root', staff_id: 4 }),
      message(3, { deleted: true, recalled: true, body: '', staff_id: 4 }),
      message(4, { author_role: 'system', body: '会话已标记为已解决' }),
      message(5, { author_role: 'user', body: 'thanks' }),
      message(6, { author_role: 'user', deleted: true, recalled: true, body: '' }),
      message(7, { author_role: 'user', deleted: true, body: '' }),
      message(8, { body: 'no id' }),
    ], { peer_read_message_id: 8 })
    const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'user', imageScope: 'user', welcome: '你好' }, global: { stubs } })
    expect(wrapper.get('[data-testid="chat-welcome"]').text()).toBe('你好')
    const authors = wrapper.findAll('[data-testid="chat-message-author"]').map((node) => node.text())
    const staff4 = 'supportChat.staffWithId:{"id":4}'
    expect(authors).toEqual(['supportChat.you', staff4, staff4, 'supportChat.you', 'supportChat.you', 'supportChat.you', 'supportChat.staff'])
    expect(wrapper.text()).not.toContain('root')
    expect(wrapper.findAll('[data-testid="chat-message-deleted"]').map((node) => node.text()))
      .toEqual(['supportChat.recalledByStaff', 'supportChat.recalledBySelf', 'supportChat.deleted'])
    expect(wrapper.get('[data-testid="chat-system"]').text()).toContain('会话已标记为已解决')
    expect(wrapper.find('[data-testid="chat-receipt"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="chat-message-delete"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="chat-message-recall"]').exists()).toBe(false)
  })

  it('offers users a recall on their own messages for two minutes', async () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-10-31T00:01:00Z'))
      const thread = await threadWith([
        message(1, { author_role: 'user', created_at: '2026-10-31T00:00:00Z' }),
        message(2, { author_role: 'user', created_at: '2026-10-30T23:50:00Z' }),
        message(3, { created_at: '2026-10-31T00:00:30Z', staff_id: 4 }),
      ])
      const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'user', imageScope: 'user', canRecall: true }, global: { stubs } })
      const recall = wrapper.findAll('[data-testid="chat-message-recall"]')
      expect(recall).toHaveLength(1)
      // Visible without hover on touch screens.
      expect(recall[0].classes()).toContain('[@media(hover:none)]:opacity-100')
      await recall[0].trigger('click')
      expect(wrapper.emitted('recall')?.[0][0]).toMatchObject({ id: 1 })
      // A message the server refused to recall loses its button at once.
      await wrapper.setProps({ recallExpiredIds: new Set([1]) })
      expect(wrapper.find('[data-testid="chat-message-recall"]').exists()).toBe(false)
      await wrapper.setProps({ recallExpiredIds: new Set() })
      vi.advanceTimersByTime(70_000)
      await flushPromises()
      expect(wrapper.find('[data-testid="chat-message-recall"]').exists()).toBe(false)
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('times the recall window by the server clock when the local clock runs ahead', async () => {
    vi.useFakeTimers()
    try {
      // The local clock is five minutes ahead of the server.
      vi.setSystemTime(new Date('2026-10-31T00:05:00Z'))
      const send = vi.fn().mockResolvedValue({ message: message(9, { author_role: 'user', body: 'hi', created_at: '2026-10-31T00:00:00Z' }), duplicate: false, chat_id: 1 })
      const thread = await threadWith([], {}, send)
      const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'user', imageScope: 'user', canRecall: true }, global: { stubs } })
      await thread.send('hi')
      await flushPromises()
      expect(wrapper.findAll('[data-testid="chat-message-recall"]')).toHaveLength(1)
      vi.advanceTimersByTime(130_000)
      await flushPromises()
      expect(wrapper.find('[data-testid="chat-message-recall"]').exists()).toBe(false)
      wrapper.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('offers a resend with the same client id and no automatic retry', async () => {
    const send = vi.fn().mockRejectedValueOnce({ reason: 'SUPPORT_CHAT_TOO_FAST', metadata: { max: 20 } })
    const thread = await threadWith([], {}, send)
    const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'user', imageScope: 'user' }, global: { stubs } })
    await thread.send('help')
    await flushPromises()
    const pending = wrapper.get('[data-testid="chat-pending"]')
    expect(pending.attributes('data-state')).toBe('failed')
    expect(wrapper.get('[data-testid="chat-pending-error"]').text()).toBe('supportChat.errors.SUPPORT_CHAT_TOO_FAST:{"max":20}')
    expect(send).toHaveBeenCalledTimes(1)

    send.mockResolvedValueOnce({ message: message(7, { author_role: 'user', body: 'help' }), duplicate: false })
    await wrapper.get('[data-testid="chat-retry"]').trigger('click')
    await flushPromises()
    expect(send.mock.calls[1][0].client_msg_id).toBe(send.mock.calls[0][0].client_msg_id)
    expect(wrapper.find('[data-testid="chat-pending"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-testid="chat-message"]')).toHaveLength(1)
  })

  it('shows staff whether the user has read each staff message', async () => {
    const list = vi.fn().mockResolvedValue(page([
      message(1, { author_role: 'user' }),
      message(2, { author_name: 'root' }),
      message(3, { author_name: 'ops' }),
      message(4, { deleted: true, body: '' }),
      message(5, { author_name: 'root' }),
      message(6, { author_role: 'user' }),
      message(7, { author_name: 'root' }),
    ], { peer_read_message_id: 3 }))
    const thread = createChatThread({ list, send: vi.fn(), markRead: vi.fn() }, { viewer: 'admin' })
    await thread.load()
    const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'admin', imageScope: 'admin' }, global: { stubs } })
    const receipts = () => wrapper.findAll('[data-testid="chat-message"]').map((row) => [row.attributes('data-id'), row.find('[data-testid="chat-receipt"]').exists() ? row.get('[data-testid="chat-receipt"]').attributes('data-state') : null])
    expect(receipts()).toEqual([['1', null], ['2', 'read'], ['3', 'read'], ['4', null], ['5', 'unread'], ['6', null], ['7', 'unread']])
    expect(wrapper.findAll('[data-testid="chat-receipt"]').map((node) => node.text())).toEqual(['supportChat.read', 'supportChat.read', 'supportChat.unread', 'supportChat.unread'])

    // The user reads on: the next poll carries the moved cursor and every staff message turns read.
    list.mockResolvedValue(page([], { peer_read_message_id: 7 }))
    await thread.poll()
    await flushPromises()
    expect(receipts().filter(([, state]) => state).map(([, state]) => state)).toEqual(['read', 'read', 'read', 'read'])
  })

  it('lets staff delete user messages, recall staff messages and shows which staff member wrote', async () => {
    const items = [
      message(1, { author_role: 'user' }),
      message(2, { author_name: 'root', staff_id: 4 }),
      message(3, { staff_id: 5 }),
      message(4, { author_role: 'user', deleted: true, recalled: true, body: '' }),
      message(5, { deleted: true, recalled: true, body: '', author_name: 'root', staff_id: 4 }),
    ]
    const thread = createChatThread({ list: vi.fn().mockResolvedValue(page(items)), send: vi.fn(), markRead: vi.fn() }, { viewer: 'admin' })
    await thread.load()
    const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'admin', imageScope: 'admin', canDelete: true }, global: { stubs } })
    expect(wrapper.findAll('[data-testid="chat-message-author"]').map((node) => node.text()))
      .toEqual(['supportChat.user', 'root', 'supportChat.staffWithId:{"id":5}', 'supportChat.user', 'root'])
    expect(wrapper.findAll('[data-testid="chat-message-deleted"]').map((node) => node.text()))
      .toEqual(['supportChat.recalledByUser', 'supportChat.recalledByStaff'])
    // Staff delete user messages and recall staff messages, at any age.
    expect(wrapper.findAll('[data-testid="chat-message-delete"]')).toHaveLength(1)
    expect(wrapper.findAll('[data-testid="chat-message-recall"]').map((node) => node.text())).toEqual(['supportChat.admin.recall', 'supportChat.admin.recall'])
    await wrapper.findAll('[data-testid="chat-message-delete"]')[0].trigger('click')
    await wrapper.findAll('[data-testid="chat-message-recall"]')[1].trigger('click')
    expect(wrapper.emitted('delete')?.map(([value]) => (value as SupportChatMessage).id)).toEqual([1, 3])
  })
})

describe('user conversation recall', () => {
  it('recalls through the API, shows the tombstone and reports a late recall', async () => {
    const sent = message(11, { author_role: 'user', body: 'sk-oops', created_at: new Date().toISOString() })
    mocks.getMyChat.mockResolvedValue({ chat: null, unread_count: 0, ticket_unread_count: 0, welcome: 'hi' })
    mocks.listMyChatMessages.mockResolvedValue(page([sent], { chat_id: 3 }))
    mocks.markMyChatRead.mockResolvedValue({ read_message_id: 11 })
    const wrapper = mount(SupportChatConversation, { props: { active: true }, global: { stubs: { ...stubs, SupportChatComposer: true } } })
    await flushPromises()
    mocks.recallMyChatMessage.mockRejectedValueOnce({ status: 409, reason: 'SUPPORT_CHAT_RECALL_EXPIRED' })
    await wrapper.get('[data-testid="chat-message-recall"]').trigger('click')
    await flushPromises()
    expect(mocks.error).toHaveBeenCalledWith(expect.stringContaining('supportChat.errors.SUPPORT_CHAT_RECALL_EXPIRED'))
    // The refused message keeps no recall button.
    expect(wrapper.find('[data-testid="chat-message-recall"]').exists()).toBe(false)
    wrapper.unmount()
    useSupportChatStore().reset()

    const fresh = message(12, { author_role: 'user', body: 'sk-oops', created_at: new Date().toISOString() })
    mocks.listMyChatMessages.mockResolvedValue(page([fresh], { chat_id: 3 }))
    const again = mount(SupportChatConversation, { props: { active: true }, global: { stubs: { ...stubs, SupportChatComposer: true } } })
    await flushPromises()
    mocks.recallMyChatMessage.mockResolvedValueOnce({ message: { ...fresh, body: '', deleted: true, recalled: true } })
    await again.get('[data-testid="chat-message-recall"]').trigger('click')
    await flushPromises()
    expect(mocks.recallMyChatMessage).toHaveBeenLastCalledWith(12)
    expect(again.get('[data-testid="chat-message-deleted"]').text()).toBe('supportChat.recalledBySelf')
    expect(again.text()).not.toContain('sk-oops')
    again.unmount()
    useSupportChatStore().reset()
  })
})

describe('chat thread polling', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  function mountPolling(thread: ChatThread) {
    return mount(defineComponent({ setup() { useChatThreadPolling(thread, computed(() => true)); return () => h('div') } }))
  }

  it('polls every 3s while mounted and stops for good after unmount, even with a poll in flight', async () => {
    vi.useFakeTimers()
    let resolveFirst!: (value: SupportChatThreadPage) => void
    const list = vi.fn()
      .mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve }))
      .mockResolvedValue(page([]))
    const thread = createChatThread({ list, send: vi.fn(), markRead: vi.fn() }, { viewer: 'user' })
    const wrapper = mountPolling(thread)
    expect(list).toHaveBeenCalledTimes(1) // the first load is in flight
    wrapper.unmount()
    resolveFirst(page([message(1)]))
    await flushPromises()
    await vi.advanceTimersByTimeAsync(CHAT_POLL_FAST_MS * 10)
    expect(list).toHaveBeenCalledTimes(1)

    // A reset after logout empties the thread; nothing polls it again.
    thread.reset()
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(CHAT_POLL_FAST_MS * 10)
    expect(list).toHaveBeenCalledTimes(1)
  })

  it('keeps polling while mounted', async () => {
    vi.useFakeTimers()
    const list = vi.fn().mockResolvedValue(page([]))
    const thread = createChatThread({ list, send: vi.fn(), markRead: vi.fn() }, { viewer: 'user' })
    const wrapper = mountPolling(thread)
    await flushPromises()
    await vi.advanceTimersByTimeAsync(CHAT_POLL_FAST_MS * 2)
    expect(list).toHaveBeenCalledTimes(3)
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(CHAT_POLL_FAST_MS * 5)
    expect(list).toHaveBeenCalledTimes(3)
  })
})

describe('chat image', () => {
  it('shows expired images without fetching them', () => {
    const wrapper = mount(SupportChatImage, { props: { image: image({ purged: true }), scope: 'user' }, global: { stubs } })
    expect(wrapper.find('[data-testid="chat-image-expired"]').exists()).toBe(true)
    expect(mocks.fetchMyChatImage).not.toHaveBeenCalled()
  })

  it('fetches with auth, opens the lightbox above dialogs and releases the URL', async () => {
    mocks.fetchAdminChatImage.mockResolvedValue(new Blob(['x']))
    const wrapper = mount(SupportChatImage, { props: { image: image(), scope: 'admin' }, global: { stubs } })
    await flushPromises()
    expect(mocks.fetchAdminChatImage).toHaveBeenCalledWith(9)
    expect(wrapper.get('img').attributes('src')).toBe('blob:preview')
    await wrapper.get('[data-testid="chat-image-open"]').trigger('click')
    expect(wrapper.get('[data-testid="lightbox"]').attributes('data-z')).toBe('60')
    wrapper.unmount()
    // Released, but kept in the idle LRU (images are no-store); a reset revokes it.
    expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    useSupportChatStore().reset()
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:preview')
  })

  it('decides failures by status only: 404/410 are final, others can retry', async () => {
    mocks.fetchMyChatImage.mockRejectedValueOnce({ status: 410, reason: 'SUPPORT_CHAT_IMAGE_GONE' })
    const gone = mount(SupportChatImage, { props: { image: image({ id: 1 }), scope: 'user' }, global: { stubs } })
    await flushPromises()
    expect(gone.find('[data-testid="chat-image-unavailable"]').exists()).toBe(true)
    expect(gone.find('[data-testid="chat-image-retry"]').exists()).toBe(false)

    mocks.fetchMyChatImage.mockRejectedValueOnce({ status: 502 }).mockResolvedValueOnce(new Blob(['x']))
    const flaky = mount(SupportChatImage, { props: { image: image({ id: 2 }), scope: 'user' }, global: { stubs } })
    await flushPromises()
    await flaky.get('[data-testid="chat-image-retry"]').trigger('click')
    await flushPromises()
    expect(flaky.get('img').attributes('src')).toBe('blob:preview')
  })
})

describe('chat composer', () => {
  it('sends with Ctrl/⌘+Enter and refuses empty or too long text', async () => {
    const wrapper = mount(SupportChatComposer, { props: { placeholder: 'p' }, global: { stubs } })
    const send = wrapper.get('[data-testid="chat-send"]')
    expect(send.attributes('disabled')).toBeDefined()
    await wrapper.get('textarea').setValue('  hi  ')
    await wrapper.get('textarea').trigger('keydown', { key: 'Enter', metaKey: true })
    expect(wrapper.emitted('send')).toEqual([[{ body: 'hi', image: null }]])
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('')
    await wrapper.get('textarea').setValue('字'.repeat(5001))
    expect(send.attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="chat-counter"]').classes()).toContain('text-red-600')
  })

  it('compresses a picked image and shows the size change', async () => {
    const compressed = { blob: new Blob(['x'], { type: 'image/webp' }), type: 'image/webp', width: 100, height: 80, originalSize: 3.2 * 1024 * 1024, size: 280 * 1024 }
    mocks.compressImage.mockResolvedValue(compressed)
    const wrapper = mount(SupportChatComposer, { props: { placeholder: 'p' }, global: { stubs } })
    const input = wrapper.get('[data-testid="chat-image-input"]')
    const file = new File(['x'], 'shot.png', { type: 'image/png' })
    Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
    await input.trigger('change')
    await flushPromises()
    expect(mocks.compressImage).toHaveBeenCalledWith(file)
    expect(wrapper.get('[data-testid="chat-image-size"]').text()).toBe('3.2 MB → 280 KB')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('send')).toEqual([[{ body: '', image: compressed }]])
    expect(wrapper.find('[data-testid="chat-image-chip"]').exists()).toBe(false)
  })

  it('takes a pasted screenshot and reports images it cannot read', async () => {
    const { ImageCompressionError } = await import('../compressImage')
    mocks.compressImage.mockRejectedValueOnce(new ImageCompressionError('decode'))
    const wrapper = mount(SupportChatComposer, { props: { placeholder: 'p' }, global: { stubs } })
    const file = new File(['x'], 'photo.heic', { type: 'image/heic' })
    await wrapper.get('textarea').trigger('paste', {
      clipboardData: { types: ['Files'], getData: () => '', items: [{ kind: 'file', getAsFile: () => file }], files: [] },
    })
    await flushPromises()
    expect(mocks.compressImage).toHaveBeenCalledWith(file)
    expect(mocks.error).toHaveBeenCalledWith('supportChat.image.errors.decode')
  })

  it('lets text win over the bitmap Office puts next to it, and attaches only text-less images', async () => {
    const wrapper = mount(SupportChatComposer, { props: { placeholder: 'p' }, global: { stubs } })
    const file = new File(['x'], 'image.png', { type: 'image/png' })
    const clipboard = (text: string) => ({
      types: ['text/plain', 'text/html', 'text/rtf', 'Files'],
      getData: (type: string) => (type === 'text/plain' ? text : type === 'text/html' ? '<table></table>' : ''),
      items: [{ kind: 'string', getAsFile: () => null }, { kind: 'file', getAsFile: () => file }],
      files: [file],
    })
    const withText = new Event('paste', { cancelable: true, bubbles: true })
    Object.defineProperty(withText, 'clipboardData', { value: clipboard('A1\tB1\nA2\tB2') })
    wrapper.get('textarea').element.dispatchEvent(withText)
    await flushPromises()
    expect(withText.defaultPrevented).toBe(false)
    expect(mocks.compressImage).not.toHaveBeenCalled()

    // Whitespace only counts as no text.
    mocks.compressImage.mockResolvedValueOnce({ blob: new Blob(['x']), type: 'image/webp', width: 1, height: 1, originalSize: 10, size: 5 })
    const blank = new Event('paste', { cancelable: true, bubbles: true })
    Object.defineProperty(blank, 'clipboardData', { value: clipboard('  \n') })
    wrapper.get('textarea').element.dispatchEvent(blank)
    await flushPromises()
    expect(blank.defaultPrevented).toBe(true)
    expect(mocks.compressImage).toHaveBeenCalledWith(file)
  })

  it("attaches an image copied with the browser's Copy Image, whose text is only its URL", async () => {
    const wrapper = mount(SupportChatComposer, { props: { placeholder: 'p' }, global: { stubs } })
    const file = new File(['x'], 'image.png', { type: 'image/png' })
    const paste = (types: string[], data: Record<string, string>) => {
      const event = new Event('paste', { cancelable: true, bubbles: true })
      Object.defineProperty(event, 'clipboardData', {
        value: { types, getData: (type: string) => data[type] ?? '', items: [{ kind: 'file', getAsFile: () => file }], files: [file] },
      })
      wrapper.get('textarea').element.dispatchEvent(event)
      return event
    }
    mocks.compressImage.mockResolvedValue({ blob: new Blob(['x']), type: 'image/webp', width: 1, height: 1, originalSize: 10, size: 5 })
    const url = 'https://example.com/error.png'
    const firefox = paste(['text/html', 'text/plain', 'Files'], {
      'text/plain': url, 'text/html': `<!--StartFragment--><img src="${url}" alt="error">`,
    })
    await flushPromises()
    expect(firefox.defaultPrevented).toBe(true)
    expect(mocks.compressImage).toHaveBeenCalledTimes(1)

    // A URL with other content, a URL in rich text, or prose keeps the text paste.
    for (const [types, data] of [
      [['text/plain', 'text/html', 'Files'], { 'text/plain': url, 'text/html': `<p>see <img src="${url}"></p>` }],
      [['text/plain', 'text/html', 'text/rtf', 'Files'], { 'text/plain': url, 'text/html': `<img src="${url}">` }],
      [['text/plain', 'Files'], { 'text/plain': `see ${url}` }],
    ] as Array<[string[], Record<string, string>]>) {
      const event = paste(types, data)
      await flushPromises()
      expect(event.defaultPrevented).toBe(false)
    }
    expect(mocks.compressImage).toHaveBeenCalledTimes(1)
  })

  it('attaches an image file copied in Finder, whose text is only the file name', async () => {
    const wrapper = mount(SupportChatComposer, { props: { placeholder: 'p' }, global: { stubs } })
    const name = 'Screenshot 2026-11-01 at 10.00.00.png'
    const file = new File(['x'], name, { type: 'image/png' })
    const paste = (text: string) => {
      const event = new Event('paste', { cancelable: true, bubbles: true })
      Object.defineProperty(event, 'clipboardData', {
        value: { types: ['text/plain', 'Files'], getData: (type: string) => (type === 'text/plain' ? text : ''), items: [{ kind: 'file', getAsFile: () => file }], files: [file] },
      })
      wrapper.get('textarea').element.dispatchEvent(event)
      return event
    }
    mocks.compressImage.mockResolvedValue({ blob: new Blob(['x']), type: 'image/webp', width: 1, height: 1, originalSize: 10, size: 5 })
    const finder = paste(name)
    await flushPromises()
    expect(finder.defaultPrevented).toBe(true)
    expect(mocks.compressImage).toHaveBeenCalledTimes(1)

    const prose = paste(`look at ${name}`)
    await flushPromises()
    expect(prose.defaultPrevented).toBe(false)
    expect(mocks.compressImage).toHaveBeenCalledTimes(1)
  })
})
