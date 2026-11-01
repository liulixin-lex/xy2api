/**
 * Support chat ("在线客服") state shared by the floating widget, the 客户服务 page
 * and the admin inbox: the signed-in user's own conversation, the chat badge
 * counts and the cache of image objectURLs.
 *
 * The ticket badge store (`supportTickets.ts`) stays ticket-only; the sidebar,
 * bubble and title add both through `useSupportBadges()`. App.vue resets this
 * store on logout and whenever the user, role or switches change.
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

  const thread = createChatThread({
    list: listMyChatMessages,
    send: sendMyChatMessage,
    markRead: markMyChatRead,
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
    try {
      const summary = await getMyChat()
      if (version !== generation || identity !== auth.user?.id || !userChatAllowed()) return
      chat.value = summary.chat
      welcome.value = summary.welcome
      userChatUnread.value = Math.max(0, summary.unread_count)
      useSupportTicketStore().setUserUnread(summary.ticket_unread_count)
      // Something changed behind an already loaded conversation.
      const state = thread.state
      if (state.loaded && summary.chat && (summary.chat.content_version !== state.contentVersion || summary.chat.last_message_id > thread.cursor())) {
        void thread.poll()
      }
    } catch {
      // The next timer tick retries.
    }
  }

  function setAdminSummary(summary: SupportChatAdminSummary) {
    adminSummary.value = summary
    adminChatWaiting.value = Math.max(0, summary.chat_waiting)
  }

  const images = new Map<string, CachedImage>()

  /** objectURL for an authenticated image; every acquire needs one release. Rejects with the API error (status kept). */
  function acquireImage(scope: SupportChatImageScope, id: number): Promise<string> {
    const key = `${scope}:${id}`
    const cached = images.get(key)
    if (cached) {
      cached.refs++
      return cached.promise
    }
    const version = generation
    const entry: CachedImage = { refs: 1, url: '', promise: Promise.resolve('') }
    entry.promise = (scope === 'admin' ? fetchAdminChatImage(id) : fetchMyChatImage(id)).then((blob) => {
      if (version !== generation || images.get(key) !== entry) throw new Error('stale image request')
      entry.url = URL.createObjectURL(blob)
      if (entry.refs <= 0) {
        URL.revokeObjectURL(entry.url)
        images.delete(key)
      }
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
    if (!cached) return
    cached.refs--
    if (cached.refs > 0) return
    if (cached.url) {
      URL.revokeObjectURL(cached.url)
      images.delete(key)
    }
  }

  function revokeImages() {
    for (const entry of images.values()) if (entry.url) URL.revokeObjectURL(entry.url)
    images.clear()
  }

  function reset() {
    generation++
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
