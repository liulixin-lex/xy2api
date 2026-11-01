import { onBeforeUnmount, reactive, watch, type Ref } from 'vue'
import {
  newSupportChatClientId,
  SUPPORT_CHAT_PAGE_SIZE,
  SUPPORT_CHAT_PAGE_SIZE_MAX,
  type SupportChatMessage,
  type SupportChatSendRequest,
  type SupportChatSendResult,
  type SupportChatStatus,
  type SupportChatThreadPage,
  type SupportChatThreadQuery,
} from '@/api/supportChat'
import type { CompressedImage } from './compressImage'

/** One side's endpoints for a single conversation (the user's own chat, or one chat in the admin inbox). */
export interface ChatThreadSource {
  list(query: SupportChatThreadQuery): Promise<SupportChatThreadPage>
  send(request: SupportChatSendRequest): Promise<SupportChatSendResult>
  markRead(lastMessageId: number): Promise<{ read_message_id: number }>
}

export interface PendingChatMessage {
  client_msg_id: string
  body: string
  image: CompressedImage | null
  /** objectURL of the compressed image, revoked when the message is sent or discarded. */
  previewUrl: string
  state: 'sending' | 'failed'
  error: unknown
  created_at: string
}

export interface ChatThreadState {
  messages: SupportChatMessage[]
  pending: PendingChatMessage[]
  status: SupportChatStatus
  blocked: boolean
  readId: number
  peerReadId: number
  contentVersion: number
  unreadCount: number
  hasOlder: boolean
  loaded: boolean
  loading: boolean
  loadingOlder: boolean
  error: unknown
  /** Date.now() of the last poll that brought a new message (polling back-off). */
  lastNewAt: number
}

export interface ChatThreadOptions {
  viewer: 'user' | 'admin'
  /** Read reports wait this long after the last change (default 1s). */
  readDelayMs?: number
  onUnreadChange?: (count: number) => void
  onSent?: (message: SupportChatMessage) => void
}

const MAX_CATCH_UP_PAGES = 20

function initialState(): ChatThreadState {
  return {
    messages: [],
    pending: [],
    status: 'open',
    blocked: false,
    readId: 0,
    peerReadId: 0,
    contentVersion: 0,
    unreadCount: 0,
    hasOlder: false,
    loaded: false,
    loading: false,
    loadingOlder: false,
    error: null,
    lastNewAt: 0,
  }
}

export type ChatThread = ReturnType<typeof createChatThread>

/**
 * Messages, cursor and pending queue for one conversation.
 *
 * Cursor rule: the poll cursor only moves on GET responses of the latest window
 * (the first load, a reload, and after_id polls). POST responses replace the
 * pending item (matched by client_msg_id) but never move the cursor, so a staff
 * message committed between the cursor and our own message is still fetched by
 * the next poll. A changed content_version (a staff member deleted a message)
 * reloads every loaded message. reset() invalidates every request in flight.
 */
export function createChatThread(source: ChatThreadSource, options: ChatThreadOptions) {
  const state = reactive<ChatThreadState>(initialState())
  const readDelay = options.readDelayMs ?? 1000
  let cursor = 0
  let generation = 0
  let inflight: Promise<void> | null = null
  let readTimer: ReturnType<typeof setTimeout> | undefined
  let reading = false
  let visible = false

  const isMine = (message: SupportChatMessage) =>
    options.viewer === 'user' ? message.author_role === 'user' : message.author_role === 'admin'

  function maxLoadedId(): number {
    return state.messages.length ? state.messages[state.messages.length - 1].id : 0
  }

  function localUnread(): number {
    if (options.viewer !== 'user') return 0
    return state.messages.filter((message) => message.author_role !== 'user' && !message.deleted && message.id > state.readId).length
  }

  function setUnread(count: number) {
    state.unreadCount = Math.max(0, count)
    options.onUnreadChange?.(state.unreadCount)
  }

  function insert(message: SupportChatMessage): boolean {
    const list = state.messages
    const existing = list.findIndex((item) => item.id === message.id)
    if (existing >= 0) {
      list.splice(existing, 1, message)
      return false
    }
    let index = list.length
    while (index > 0 && list[index - 1].id > message.id) index--
    list.splice(index, 0, message)
    return true
  }

  function dropPending(clientId: string | undefined) {
    if (!clientId) return
    const index = state.pending.findIndex((item) => item.client_msg_id === clientId)
    if (index < 0) return
    const [removed] = state.pending.splice(index, 1)
    if (removed.previewUrl) URL.revokeObjectURL(removed.previewUrl)
  }

  function merge(items: SupportChatMessage[]): boolean {
    let added = false
    for (const item of items) {
      if (insert(item) && !isMine(item)) added = true
      dropPending(item.client_msg_id)
    }
    return added
  }

  function applyMeta(page: SupportChatThreadPage) {
    state.status = page.status
    state.blocked = page.blocked
    state.readId = Math.max(state.readId, page.read_message_id)
    state.peerReadId = Math.max(state.peerReadId, page.peer_read_message_id)
    if (options.viewer === 'user') setUnread(page.unread_count)
  }

  /** Runs one GET sequence at a time; overlapping callers share it. */
  function exclusive(run: (version: number) => Promise<void>): Promise<void> {
    if (inflight) return inflight
    const version = generation
    const promise = run(version).finally(() => {
      if (inflight === promise) inflight = null
    })
    inflight = promise
    return promise
  }

  async function catchUp(version: number, from: number, collected?: SupportChatMessage[]): Promise<SupportChatThreadPage | null> {
    let after = from
    let last: SupportChatThreadPage | null = null
    for (let page = 0; page < MAX_CATCH_UP_PAGES; page++) {
      const result = await source.list({ after_id: after, limit: SUPPORT_CHAT_PAGE_SIZE_MAX })
      if (version !== generation) return null
      last = result
      if (collected) collected.push(...result.items)
      else if (merge(result.items)) state.lastNewAt = Date.now()
      if (result.items.length) {
        after = result.items[result.items.length - 1].id
        cursor = Math.max(cursor, after)
      }
      if (!result.has_more || !result.items.length) break
    }
    return last
  }

  /** First load or full reload of the latest window. */
  function load(): Promise<void> {
    return exclusive(async (version) => {
      state.loading = true
      state.error = null
      try {
        const page = await source.list({ limit: SUPPORT_CHAT_PAGE_SIZE })
        if (version !== generation) return
        state.messages = []
        merge(page.items)
        cursor = maxLoadedId()
        state.hasOlder = page.has_more
        state.contentVersion = page.content_version
        applyMeta(page)
        state.loaded = true
        state.lastNewAt = Date.now()
        scheduleRead()
      } catch (err) {
        if (version === generation) state.error = err
      } finally {
        if (version === generation) state.loading = false
      }
    })
  }

  /** Re-reads every loaded message after a staff deletion. */
  async function reloadWindow(version: number) {
    const oldest = state.messages.length ? state.messages[0].id : 0
    const collected: SupportChatMessage[] = []
    const page = await catchUp(version, Math.max(0, oldest - 1), collected)
    if (!page || version !== generation) return
    state.messages = []
    merge(collected)
    state.contentVersion = page.content_version
    applyMeta(page)
  }

  /** Fetches what is newer than the cursor, following has_more. */
  function poll(): Promise<void> {
    if (!state.loaded) return load()
    return exclusive(async (version) => {
      try {
        const page = await catchUp(version, cursor)
        if (!page || version !== generation) return
        state.error = null
        if (page.content_version !== state.contentVersion) await reloadWindow(version)
        else applyMeta(page)
        scheduleRead()
      } catch (err) {
        if (version === generation) state.error = err
      }
    })
  }

  async function loadOlder() {
    if (!state.loaded || !state.hasOlder || state.loadingOlder || !state.messages.length) return
    const version = generation
    state.loadingOlder = true
    try {
      const page = await source.list({ before_id: state.messages[0].id, limit: SUPPORT_CHAT_PAGE_SIZE })
      if (version !== generation) return
      merge(page.items)
      state.hasOlder = page.has_more
    } catch (err) {
      if (version === generation) state.error = err
    } finally {
      if (version === generation) state.loadingOlder = false
    }
  }

  async function deliver(item: PendingChatMessage): Promise<boolean> {
    const version = generation
    item.state = 'sending'
    item.error = null
    try {
      const result = await source.send({ body: item.body, client_msg_id: item.client_msg_id, image: item.image?.blob ?? null })
      if (version !== generation) return false
      insert(result.message)
      dropPending(item.client_msg_id)
      // A user message reopens a resolved chat on the server too.
      if (options.viewer === 'user') state.status = 'open'
      options.onSent?.(result.message)
      scheduleRead()
      return true
    } catch (err) {
      if (version !== generation) return false
      item.state = 'failed'
      item.error = err
      return false
    }
  }

  /** Queues a message and posts it once; a failure stays in the list for a manual retry. */
  function send(body: string, image: CompressedImage | null = null): Promise<boolean> {
    const item: PendingChatMessage = reactive({
      client_msg_id: newSupportChatClientId(),
      body,
      image,
      previewUrl: image ? URL.createObjectURL(image.blob) : '',
      state: 'sending',
      error: null,
      created_at: new Date().toISOString(),
    })
    state.pending.push(item)
    return deliver(item)
  }

  /** Retries with the SAME client_msg_id, so a request that did reach the server is not stored twice. */
  function retry(clientId: string): Promise<boolean> {
    const item = state.pending.find((candidate) => candidate.client_msg_id === clientId)
    if (!item || item.state === 'sending') return Promise.resolve(false)
    return deliver(item)
  }

  function discard(clientId: string) {
    const item = state.pending.find((candidate) => candidate.client_msg_id === clientId)
    if (item && item.state === 'failed') dropPending(clientId)
  }

  /** Replaces one message in place, e.g. the tombstone returned by a staff delete. */
  function replace(message: SupportChatMessage) {
    const index = state.messages.findIndex((item) => item.id === message.id)
    if (index >= 0) state.messages.splice(index, 1, message)
  }

  function scheduleRead() {
    if (!visible || (typeof document !== 'undefined' && document.visibilityState === 'hidden')) return
    if (maxLoadedId() <= state.readId) return
    if (readTimer) clearTimeout(readTimer)
    const version = generation
    readTimer = setTimeout(async () => {
      readTimer = undefined
      const target = maxLoadedId()
      if (version !== generation || reading || !visible || target <= state.readId) return
      reading = true
      try {
        const result = await source.markRead(target)
        if (version !== generation) return
        state.readId = Math.max(state.readId, result.read_message_id)
        if (options.viewer === 'user') setUnread(localUnread())
      } catch {
        // The next change or visibility switch tries again.
      } finally {
        reading = false
      }
    }, readDelay)
  }

  /** The conversation is on screen (panel open on 消息, or the chat page tab); read reports only go out while true. */
  function setVisible(value: boolean) {
    visible = value
    if (value) scheduleRead()
    else if (readTimer) {
      clearTimeout(readTimer)
      readTimer = undefined
    }
  }

  function reset() {
    generation++
    inflight = null
    reading = false
    cursor = 0
    if (readTimer) clearTimeout(readTimer)
    readTimer = undefined
    for (const item of state.pending) if (item.previewUrl) URL.revokeObjectURL(item.previewUrl)
    Object.assign(state, initialState())
  }

  return {
    state,
    load,
    poll,
    loadOlder,
    send,
    retry,
    discard,
    replace,
    setVisible,
    scheduleRead,
    reset,
    cursor: () => cursor,
    maxLoadedId,
  }
}

export const CHAT_POLL_FAST_MS = 3000
export const CHAT_POLL_SLOW_MS = 8000
export const CHAT_POLL_SLOWDOWN_AFTER_MS = 120_000

/**
 * Polls an open conversation: every 3s, every 8s after two quiet minutes, never
 * while the tab is hidden, and right away when the tab becomes visible again.
 */
export function useChatThreadPolling(thread: ChatThread, active: Ref<boolean>) {
  let timer: ReturnType<typeof setTimeout> | undefined

  function stop() {
    if (timer) clearTimeout(timer)
    timer = undefined
  }

  function schedule() {
    stop()
    if (!active.value || document.visibilityState === 'hidden') return
    const quiet = Date.now() - (thread.state.lastNewAt || Date.now())
    timer = setTimeout(tick, quiet >= CHAT_POLL_SLOWDOWN_AFTER_MS ? CHAT_POLL_SLOW_MS : CHAT_POLL_FAST_MS)
  }

  async function tick() {
    timer = undefined
    if (!active.value || document.visibilityState === 'hidden') return
    await thread.poll()
    schedule()
  }

  function onVisibility() {
    if (document.visibilityState === 'visible' && active.value) void tick()
    else stop()
  }

  watch(active, (value) => {
    thread.setVisible(value)
    if (value) void tick()
    else stop()
  }, { immediate: true })

  document.addEventListener('visibilitychange', onVisibility)
  onBeforeUnmount(() => {
    stop()
    thread.setVisible(false)
    document.removeEventListener('visibilitychange', onVisibility)
  })
}
