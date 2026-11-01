/**
 * Support chat ("在线客服") state shared by the floating widget, the 客户服务 page
 * and the admin inbox: the signed-in user's own conversation, the chat badge
 * counts and the cache of image objectURLs.
 *
 * The ticket badge store (`supportTickets.ts`) stays ticket-only; the sidebar,
 * bubble and title add both through `useSupportBadges()`. App.vue resets this
 * store on logout and whenever the user, role or switches change.
 *
 * Chat images are served with `Cache-Control: no-store`, so an objectURL whose
 * last user released it is parked in a small LRU (IDLE_IMAGE_LIMIT) instead of
 * being revoked at once; reopening a conversation then needs no new download.
 * Evicted entries and everything on reset() are revoked.
 */
import { defineStore } from 'pinia'
import { ref } from 'vue'
import {
  fetchAdminChatImage,
  fetchMyChatImage,
  getMyChat,
  listMyChatMessages,
  markMyChatRead,
  sendMyChatMessage,
  type SupportChatAdminSummary,
  type SupportChatUserChat,
} from '@/api/supportChat'
import { createChatThread } from '@/features/support-chat/chatThread'
import { FeatureFlags, resolveFeatureFlag } from '@/utils/featureFlags'
import { useAppStore } from './app'
import { useAuthStore } from './auth'
import { useSupportTicketStore } from './supportTickets'

export type SupportChatImageScope = 'user' | 'admin'

/** objectURLs kept after their last release, oldest evicted first. */
export const IDLE_IMAGE_LIMIT = 40

interface CachedImage {
  refs: number
  promise: Promise<string>
  url: string
}

export const useSupportChatStore = defineStore('supportChat', () => {
  /** Staff replies the user has not read (user side). */
  const userChatUnread = ref(0)
  /** Chats waiting for a staff reply (staff side). */
  const adminChatWaiting = ref(0)
  const welcome = ref('')
  const chat = ref<SupportChatUserChat | null>(null)
  const adminSummary = ref<SupportChatAdminSummary | null>(null)
  /** Date.now() of the user's last sent message; the collapsed widget polls faster for a while after it. */
  const lastSentAt = ref(0)
  let generation = 0
  /** Chat id the loaded thread belongs to, learned from the summary (0 = not known yet). */
  let threadChatId = 0

  // Every request re-checks that the user may chat, so a poll that outlives a
  // logout or a switch change never goes out without a token.
  const thread = createChatThread({
    list: (query) => (userChatAllowed() ? listMyChatMessages(query) : Promise.reject(new Error('support chat unavailable'))),
    send: (request) => (userChatAllowed() ? sendMyChatMessage(request) : Promise.reject(new Error('support chat unavailable'))),
    markRead: (id) => (userChatAllowed() ? markMyChatRead(id) : Promise.reject(new Error('support chat unavailable'))),
  }, {
    viewer: 'user',
    onUnreadChange: (count) => { userChatUnread.value = count },
    onSent: () => { lastSentAt.value = Date.now() },
  })

  /** The signed-in user's own conversation (a plain object kept out of Pinia state). */
  function userThread() {
    return thread
  }

  function userChatAllowed(): boolean {
    const auth = useAuthStore()
    const app = useAppStore()
    return auth.isAuthenticated && !auth.canAccessAdmin && resolveFeatureFlag(app.cachedPublicSettings, FeatureFlags.supportChat)
  }

  /** One GET /support-chat: chat badge, ticket badge and welcome text in a single request. */
  async function refreshSummary() {
    if (!userChatAllowed()) return
    const auth = useAuthStore()
    const version = generation
    const identity = auth.user?.id
    const cursorAtStart = thread.cursor()
    try {
      const summary = await getMyChat()
      if (version !== generation || identity !== auth.user?.id || !userChatAllowed()) return
      chat.value = summary.chat
      welcome.value = summary.welcome
      userChatUnread.value = Math.max(0, summary.unread_count)
      useSupportTicketStore().setUserUnread(summary.ticket_unread_count)
      syncThread(summary.chat, cursorAtStart)
    } catch {
      // The next timer tick retries.
    }
  }

  /**
   * Something changed behind an already loaded conversation. A missing chat, a
   * different chat id, or a last_message_id below what the thread already read
   * means staff deleted the whole chat (a blocked user gets a fresh empty row):
   * drop the window and load again. Otherwise poll for new or deleted messages.
   */
  function syncThread(current: SupportChatUserChat | null, cursorAtStart: number) {
    const state = thread.state
    const id = current?.id ?? 0
    if (!state.loaded) {
      // The next load shows this chat; remember it so a later replacement is seen.
      threadChatId = id
      return
    }
    const cursor = thread.cursor()
    // Compare with what the thread had read when the summary was requested: a
    // poll running alongside may have fetched messages the summary never saw.
    const seen = Math.min(cursorAtStart, cursor)
    // Sent messages join state.messages without moving the cursor, so a thread
    // holding only the user's own sends still counts as loaded content.
    const loadedChat = thread.chatId()
    const replaced = (threadChatId !== 0 && id !== threadChatId) ||
      (loadedChat !== 0 && id !== loadedChat) ||
      (!current && (seen > 0 || state.messages.length > 0)) ||
      (!!current && current.last_message_id < seen)
    threadChatId = id
    if (replaced) {
      void thread.reload()
      return
    }
    if (current && (current.content_version !== state.contentVersion || current.last_message_id > cursor)) {
      void thread.poll()
    }
  }

  function setAdminSummary(summary: SupportChatAdminSummary) {
    adminSummary.value = summary
    adminChatWaiting.value = Math.max(0, summary.chat_waiting)
  }

  const images = new Map<string, CachedImage>()
  /** Keys of loaded images nobody holds, least recently released first. */
  const idleImages = new Set<string>()

  function parkImage(key: string) {
    idleImages.delete(key)
    idleImages.add(key)
    while (idleImages.size > IDLE_IMAGE_LIMIT) {
      const oldest = idleImages.values().next().value as string
      idleImages.delete(oldest)
      const entry = images.get(oldest)
      if (entry?.url) URL.revokeObjectURL(entry.url)
      images.delete(oldest)
    }
  }

  /** objectURL for an authenticated image; every acquire needs one release. Rejects with the API error (status kept). */
  function acquireImage(scope: SupportChatImageScope, id: number): Promise<string> {
    const key = `${scope}:${id}`
    const cached = images.get(key)
    if (cached) {
      idleImages.delete(key)
      cached.refs++
      return cached.promise
    }
    const version = generation
    const entry: CachedImage = { refs: 1, url: '', promise: Promise.resolve('') }
    entry.promise = (scope === 'admin' ? fetchAdminChatImage(id) : fetchMyChatImage(id)).then((blob) => {
      if (version !== generation || images.get(key) !== entry) throw new Error('stale image request')
      entry.url = URL.createObjectURL(blob)
      if (entry.refs <= 0) parkImage(key)
      return entry.url
    }, (err) => {
      if (images.get(key) === entry) images.delete(key)
      throw err
    })
    images.set(key, entry)
    return entry.promise
  }

  function releaseImage(scope: SupportChatImageScope, id: number) {
    const key = `${scope}:${id}`
    const cached = images.get(key)
    if (!cached || cached.refs <= 0) return
    cached.refs--
    if (cached.refs > 0) return
    if (cached.url) parkImage(key)
  }

  function revokeImages() {
    for (const entry of images.values()) if (entry.url) URL.revokeObjectURL(entry.url)
    images.clear()
    idleImages.clear()
  }

  function reset() {
    generation++
    threadChatId = 0
    thread.reset()
    revokeImages()
    userChatUnread.value = 0
    adminChatWaiting.value = 0
    welcome.value = ''
    chat.value = null
    adminSummary.value = null
    lastSentAt.value = 0
  }

  return {
    userChatUnread,
    adminChatWaiting,
    welcome,
    chat,
    adminSummary,
    lastSentAt,
    userThread,
    refreshSummary,
    setAdminSummary,
    acquireImage,
    releaseImage,
    reset,
  }
})
