import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, defineComponent, h } from 'vue'
import type { SupportChatImageRef, SupportChatMessage, SupportChatThreadPage } from '@/api/supportChat'
import { useSupportChatStore } from '@/stores/supportChat'
import { CHAT_POLL_FAST_MS, createChatThread, useChatThreadPolling, type ChatThread } from '../chatThread'
import SupportChatThread from '../SupportChatThread.vue'
import SupportChatImage from '../SupportChatImage.vue'
import SupportChatComposer from '../SupportChatComposer.vue'

const mocks = vi.hoisted(() => ({
  fetchMyChatImage: vi.fn(), fetchAdminChatImage: vi.fn(), compressImage: vi.fn(), error: vi.fn(),
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }),
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.error, showSuccess: vi.fn() }) }))
vi.mock('@/api/supportChat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportChat')>()),
  fetchMyChatImage: mocks.fetchMyChatImage, fetchAdminChatImage: mocks.fetchAdminChatImage,
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

  it('names the sides, hides deleted content and shows the read receipt on my last message', async () => {
    const thread = await threadWith([
      message(1, { author_role: 'user' }),
      message(2, { author_name: 'root' }),
      message(3, { deleted: true, body: '' }),
      message(4, { author_role: 'system', body: '会话已标记为已解决' }),
      message(5, { author_role: 'user', body: 'thanks' }),
    ], { peer_read_message_id: 5 })
    const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'user', imageScope: 'user', welcome: '你好' }, global: { stubs } })
    expect(wrapper.get('[data-testid="chat-welcome"]').text()).toBe('你好')
    const authors = wrapper.findAll('[data-testid="chat-message-author"]').map((node) => node.text())
    expect(authors).toEqual(['supportChat.you', 'supportChat.staff', 'supportChat.staff', 'supportChat.you'])
    expect(wrapper.text()).not.toContain('root')
    expect(wrapper.get('[data-testid="chat-message-deleted"]').text()).toBe('supportChat.deleted')
    expect(wrapper.get('[data-testid="chat-system"]').text()).toContain('会话已标记为已解决')
    const receipts = wrapper.findAll('[data-testid="chat-receipt"]')
    expect(receipts).toHaveLength(1)
    expect(receipts[0].text()).toBe('supportChat.read')
    expect(wrapper.find('[data-testid="chat-message-delete"]').exists()).toBe(false)
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

  it('lets staff delete messages and shows which staff member wrote', async () => {
    const thread = createChatThread({ list: vi.fn().mockResolvedValue(page([message(1, { author_role: 'user' }), message(2, { author_name: 'root' })])), send: vi.fn(), markRead: vi.fn() }, { viewer: 'admin' })
    await thread.load()
    const wrapper = mount(SupportChatThread, { props: { thread, viewer: 'admin', imageScope: 'admin', canDelete: true }, global: { stubs } })
    expect(wrapper.findAll('[data-testid="chat-message-author"]').map((node) => node.text())).toEqual(['supportChat.user', 'root'])
    await wrapper.findAll('[data-testid="chat-message-delete"]')[0].trigger('click')
    expect(wrapper.emitted('delete')?.[0][0]).toMatchObject({ id: 1 })
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
