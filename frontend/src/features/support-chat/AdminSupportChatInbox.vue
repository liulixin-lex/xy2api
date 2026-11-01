<template>
  <div class="flex flex-col gap-4 lg:h-[calc(100vh-64px-4rem)]" data-testid="chat-inbox">
    <slot name="header" />

    <div class="grid min-h-0 flex-1 gap-4 lg:grid-cols-[20rem_minmax(0,1fr)_18rem]">
      <!-- List -->
      <section v-show="wide || !selectedId" class="card flex min-h-[24rem] flex-col overflow-hidden lg:min-h-0" data-testid="chat-inbox-list">
        <div class="space-y-3 border-b border-gray-100 p-3 dark:border-dark-700">
          <div class="flex flex-wrap gap-1" role="group" :aria-label="t('supportChat.admin.filterLabel')">
            <button
              v-for="option in filterOptions"
              :key="option.value"
              type="button"
              class="rounded-lg px-2.5 py-1 text-xs font-medium transition-colors"
              :class="filter === option.value
                ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
                : 'text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-white'"
              :aria-pressed="filter === option.value"
              data-testid="chat-filter"
              :data-value="option.value"
              @click="setFilter(option.value)"
            >{{ option.label }}</button>
          </div>
          <input
            v-model="keyword"
            type="search"
            class="input text-sm"
            :placeholder="t('supportChat.admin.keywordPlaceholder')"
            :aria-label="t('supportChat.admin.keywordPlaceholder')"
            data-testid="chat-keyword"
            @input="onKeyword"
          />
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto">
          <div v-if="listLoading && !chats.length" class="space-y-2 p-3" aria-busy="true">
            <div v-for="index in 4" :key="index" class="h-14 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800"></div>
          </div>
          <p v-else-if="!chats.length" class="p-6 text-center text-sm text-gray-500 dark:text-gray-400" data-testid="chat-inbox-empty">
            {{ t('supportChat.admin.empty') }}
          </p>
          <ul v-else>
            <li v-for="chat in chats" :key="chat.id">
              <button
                type="button"
                class="block w-full border-b border-gray-50 px-3 py-2.5 text-left transition-colors hover:bg-gray-50 dark:border-dark-800 dark:hover:bg-dark-800"
                :class="chat.id === selectedId ? 'bg-primary-50/60 dark:bg-primary-900/20' : ''"
                data-testid="chat-row"
                :data-id="chat.id"
                @click="select(chat.id)"
              >
                <div class="flex items-center gap-2">
                  <span v-if="chat.unread" class="h-2 w-2 shrink-0 rounded-full bg-red-500" :title="t('supportChat.admin.unread')" data-testid="chat-row-unread"></span>
                  <span class="min-w-0 flex-1 truncate text-sm font-medium" :class="chat.user.deleted ? 'text-gray-400 line-through' : 'text-gray-900 dark:text-white'">
                    {{ chat.user.email || chat.user.username || `#${chat.user_id}` }}
                  </span>
                  <Icon v-if="chat.blocked" name="ban" size="xs" class="shrink-0 text-red-500" :title="t('supportChat.admin.blockedBadge')" />
                  <span class="shrink-0 text-[11px] text-gray-400">{{ formatRelativeTime(chat.last_message_at) }}</span>
                </div>
                <p class="mt-0.5 truncate text-xs text-gray-500 dark:text-gray-400">{{ previewText(chat) }}</p>
                <p v-if="chat.status === 'open' && chat.waiting_since" class="mt-0.5 text-[11px] font-medium text-amber-600 dark:text-amber-400" data-testid="chat-row-waiting">
                  {{ t('supportChat.admin.waitingFor', { minutes: minutesSince(chat.waiting_since, now) }) }}
                </p>
                <p v-else-if="chat.status === 'resolved'" class="mt-0.5 text-[11px] text-gray-400">{{ t('supportChat.status.resolved') }}</p>
              </button>
            </li>
          </ul>
        </div>
        <div v-if="total > pageSize" class="flex items-center justify-between border-t border-gray-100 px-3 py-2 text-xs text-gray-500 dark:border-dark-700">
          <button type="button" class="btn btn-secondary btn-sm" :disabled="page <= 1" @click="changePage(page - 1)">{{ t('supportChat.admin.prev') }}</button>
          <span>{{ page }} / {{ Math.ceil(total / pageSize) }}</span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="page * pageSize >= total" @click="changePage(page + 1)">{{ t('supportChat.admin.next') }}</button>
        </div>
      </section>

      <!-- Thread -->
      <section v-show="wide || selectedId" class="card flex min-h-[32rem] flex-col overflow-hidden lg:min-h-0" data-testid="chat-inbox-thread">
        <template v-if="selectedId">
          <header class="flex flex-wrap items-center gap-2 border-b border-gray-100 px-4 py-3 dark:border-dark-700">
            <button
              v-if="!wide"
              type="button"
              class="-ml-1 rounded-lg p-1 text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-700 dark:hover:text-white"
              :aria-label="t('supportChat.admin.back')"
              data-testid="chat-back"
              @click="select(0)"
            >
              <Icon name="arrowLeft" size="md" />
            </button>
            <div class="min-w-0 flex-1">
              <p class="truncate text-sm font-semibold text-gray-900 dark:text-white">{{ detail?.user.email || detail?.user.username || `#${selectedId}` }}</p>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ detail ? t(`supportChat.status.${detail.status}`) : '' }}<template v-if="detail?.blocked"> · {{ t('supportChat.admin.blockedBadge') }}</template>
              </p>
            </div>
            <div v-if="canManage && detail" class="flex flex-wrap items-center gap-2">
              <button
                v-if="detail.status === 'open'"
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="busy"
                data-testid="chat-resolve"
                @click="resolve"
              >{{ t('supportChat.admin.resolve') }}</button>
              <button v-else type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="chat-reopen" @click="reopen">
                {{ t('supportChat.admin.reopen') }}
              </button>
              <button
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="busy"
                :aria-pressed="detail.blocked"
                data-testid="chat-block"
                @click="toggleBlocked"
              >{{ detail.blocked ? t('supportChat.admin.unblock') : t('supportChat.admin.block') }}</button>
              <button type="button" class="btn btn-danger btn-sm" :disabled="busy" data-testid="chat-delete" @click="confirm = { kind: 'chat' }">
                {{ t('supportChat.admin.deleteChat') }}
              </button>
            </div>
          </header>
          <SupportChatThread
            :thread="thread"
            viewer="admin"
            image-scope="admin"
            :can-delete="canManage"
            :empty-text="t('supportChat.admin.noMessages')"
            @delete="(message) => confirm = { kind: 'message', message }"
          />
          <SupportChatComposer v-if="canManage" :placeholder="t('supportChat.admin.replyPlaceholder')" @send="onSend" />
        </template>
        <div v-else class="flex flex-1 items-center justify-center p-6 text-sm text-gray-500 dark:text-gray-400" data-testid="chat-inbox-placeholder">
          {{ t('supportChat.admin.pick') }}
        </div>
      </section>

      <!-- User card -->
      <aside v-if="detail && (wide || selectedId)" class="space-y-4 lg:min-h-0 lg:overflow-y-auto">
        <AdminSupportUserCard :user="detail.user" :user-id="detail.user_id">
          <p v-if="detail.blocked && detail.blocked_reason" class="mt-3 rounded-lg bg-red-50 px-2 py-1 text-xs text-red-700 dark:bg-red-900/30 dark:text-red-300">
            {{ detail.blocked_reason }}
          </p>
        </AdminSupportUserCard>
      </aside>
    </div>

    <ConfirmDialog
      :show="!!confirm"
      :title="confirmText.title"
      :message="confirmText.message"
      :confirm-text="confirmText.action"
      danger
      @confirm="runConfirm"
      @cancel="confirm = null"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import AdminSupportUserCard from '@/features/support-tickets/AdminSupportUserCard.vue'
import { useSupportTicketRequests } from '@/features/support-tickets/useSupportTicketRequests'
import {
  deleteAdminChat,
  deleteAdminChatMessage,
  getAdminChat,
  listAdminChatMessages,
  listAdminChats,
  markAdminChatRead,
  sendAdminChatMessage,
  setAdminChatBlocked,
  setAdminChatStatus,
  type SupportChatAdminFilter,
  type SupportChatAdminItem,
  type SupportChatMessage,
} from '@/api/supportChat'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { useAppStore } from '@/stores/app'
import { useSupportChatStore } from '@/stores/supportChat'
import { extractApiErrorCode } from '@/utils/apiError'
import { formatRelativeTime } from '@/utils/format'
import { createChatThread } from './chatThread'
import type { CompressedImage } from './compressImage'
import SupportChatComposer from './SupportChatComposer.vue'
import SupportChatThread from './SupportChatThread.vue'
import { minutesSince, supportChatErrorMessage } from './supportChat'
import { watchStaffSummaryFast } from './useSupportStaffAlerts'

const WIDE_QUERY = '(min-width: 1024px)'

type Confirm = { kind: 'chat' } | { kind: 'message'; message: SupportChatMessage }

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const chatStore = useSupportChatStore()
const { canView, canManage } = useAdminPermission('support_tickets')

const pageSize = 20
const filter = ref<SupportChatAdminFilter>('waiting')
const keyword = ref('')
const page = ref(1)
const total = ref(0)
const chats = ref<SupportChatAdminItem[]>([])
const detail = ref<SupportChatAdminItem | null>(null)
const busy = ref(false)
const confirm = ref<Confirm | null>(null)
// Removing a staff message is a recall; removing a user message is a delete.
const confirmText = computed(() => {
  const value = confirm.value
  if (value?.kind === 'chat') return { title: t('supportChat.admin.deleteChatTitle'), message: t('supportChat.admin.deleteChatConfirm'), action: t('supportChat.admin.confirmDelete') }
  if (value?.kind === 'message' && value.message.author_role === 'admin') {
    return { title: t('supportChat.admin.recallTitle'), message: t('supportChat.admin.recallConfirm'), action: t('supportChat.admin.recall') }
  }
  return { title: t('supportChat.admin.deleteMessageTitle'), message: t('supportChat.admin.deleteMessageConfirm'), action: t('supportChat.admin.confirmDelete') }
})
const now = ref(Date.now())
const wide = ref(true)
let keywordTimer: ReturnType<typeof setTimeout> | undefined
let clock: ReturnType<typeof setInterval> | undefined
let media: MediaQueryList | null = null
let releaseFast: (() => void) | null = null
let detailSequence = 0

const requests = useSupportTicketRequests(() => JSON.stringify([filter.value, keyword.value, page.value]), () => {
  chats.value = []
  total.value = 0
}, () => canView.value)
const listLoading = requests.loading

const selectedId = computed(() => {
  const value = Number(route.query?.chat)
  return Number.isInteger(value) && value > 0 ? value : 0
})

let threadChatId = 0
const thread = createChatThread({
  list: (query) => listAdminChatMessages(threadChatId, query),
  send: (request) => sendAdminChatMessage(threadChatId, request),
  markRead: (lastId) => markAdminChatRead(threadChatId, lastId),
}, { viewer: 'admin' })

const filterOptions = computed<Array<{ value: SupportChatAdminFilter; label: string }>>(() => [
  { value: 'waiting', label: t('supportChat.admin.filters.waiting') },
  { value: 'open', label: t('supportChat.admin.filters.open') },
  { value: 'resolved', label: t('supportChat.admin.filters.resolved') },
  { value: 'all', label: t('supportChat.admin.filters.all') },
])

function previewText(chat: SupportChatAdminItem) {
  if (chat.preview_kind === 'image') return t('supportChat.admin.previewImage')
  if (chat.preview_kind === 'recalled') return t('supportChat.admin.previewRecalled')
  if (chat.preview_kind === 'deleted') return t('supportChat.admin.previewDeleted')
  if (chat.preview_kind === 'none') return t('supportChat.admin.previewNone')
  return chat.preview
}

/** true when this read was still the newest list read and `chats` now holds its rows. */
async function loadList(): Promise<boolean> {
  const request = requests.begin()
  if (!request) return false
  try {
    const result = await listAdminChats({
      filter: filter.value,
      keyword: keyword.value.trim() || undefined,
      page: page.value,
      page_size: pageSize,
    })
    if (!request.current()) return false
    chats.value = result.items
    total.value = result.total
    return true
  } catch (err) {
    if (request.current()) appStore.showError(supportChatErrorMessage(err, t, 'supportChat.loadFailed'))
    return false
  } finally {
    request.finish()
  }
}

async function loadDetail() {
  const id = selectedId.value
  const sequence = ++detailSequence
  if (!id || !canView.value) {
    detail.value = null
    return
  }
  try {
    const result = await getAdminChat(id)
    if (sequence === detailSequence && id === selectedId.value) detail.value = result
  } catch (err) {
    if (sequence !== detailSequence) return
    appStore.showError(supportChatErrorMessage(err, t, 'supportChat.loadFailed'))
    if (extractApiErrorCode(err) === 'SUPPORT_CHAT_NOT_FOUND') void select(0)
  }
}

function openThread(id: number) {
  threadChatId = id
  thread.reset()
  detail.value = null
  if (!id) return
  thread.setVisible(true)
  void thread.load()
  void loadDetail()
}

function select(id: number) {
  const query = { ...route.query, tab: 'chats' } as Record<string, string | undefined>
  if (id) query.chat = String(id)
  else delete query.chat
  return router.replace({ query })
}

function setFilter(value: SupportChatAdminFilter) {
  if (filter.value === value) return
  filter.value = value
  page.value = 1
  void loadList()
}

function onKeyword() {
  if (keywordTimer) clearTimeout(keywordTimer)
  keywordTimer = setTimeout(() => {
    page.value = 1
    void loadList()
  }, 300)
}

function changePage(value: number) {
  page.value = value
  void loadList()
}

function onSend(payload: { body: string; image: CompressedImage | null }) {
  if (!canManage.value) return
  void thread.send(payload.body, payload.image)
}

/**
 * The thread's poll cursor: every message up to it was fetched by GET. The server
 * refuses to resolve when a user message newer than this exists. Not the newest
 * loaded id: our own POSTed reply can be newer than a user message not polled yet.
 */
function lastSeenMessageId(): number {
  return thread.cursor()
}

async function act(action: () => Promise<SupportChatAdminItem>, successKey: string) {
  if (!canManage.value || busy.value) return
  const id = selectedId.value
  busy.value = true
  try {
    const result = await action()
    if (id !== selectedId.value) return
    // Drop older detail reads still in flight; they predate this action.
    detailSequence++
    detail.value = result
    appStore.showSuccess(t(successKey))
    void loadList()
  } catch (err) {
    if (id !== selectedId.value) return
    if (extractApiErrorCode(err) === 'SUPPORT_CHAT_CHANGED') {
      appStore.showWarning(t('supportChat.admin.changed'))
      void thread.poll()
      void loadDetail()
    } else {
      appStore.showError(supportChatErrorMessage(err, t))
    }
  } finally {
    busy.value = false
  }
}

function resolve() {
  const id = selectedId.value
  return act(() => setAdminChatStatus(id, 'resolved', lastSeenMessageId()), 'supportChat.admin.resolved')
}

function reopen() {
  const id = selectedId.value
  return act(() => setAdminChatStatus(id, 'open', lastSeenMessageId()), 'supportChat.admin.reopened')
}

function toggleBlocked() {
  const id = selectedId.value
  const blocked = !detail.value?.blocked
  return act(() => setAdminChatBlocked(id, blocked), blocked ? 'supportChat.admin.blockedDone' : 'supportChat.admin.unblockedDone')
}

async function runConfirm() {
  const pending = confirm.value
  confirm.value = null
  if (!pending || !canManage.value) return
  const id = selectedId.value
  busy.value = true
  try {
    if (pending.kind === 'message') {
      const result = await deleteAdminChatMessage(id, pending.message.id)
      if (id === selectedId.value) thread.replace(result.message)
      appStore.showSuccess(t(pending.message.author_role === 'admin' ? 'supportChat.admin.messageRecalled' : 'supportChat.admin.messageDeleted'))
    } else {
      await deleteAdminChat(id)
      appStore.showSuccess(t('supportChat.admin.chatDeleted'))
      if (id === selectedId.value) await select(0)
      void loadList()
    }
  } catch (err) {
    appStore.showError(supportChatErrorMessage(err, t))
  } finally {
    busy.value = false
  }
}

function onMedia() {
  wide.value = !!media?.matches
}

function onVisibility() {
  if (document.visibilityState === 'visible') thread.scheduleRead()
}

// The App-level poller refreshes the summary; the inbox only refetches when something changed.
// List rows carry the same fields as the detail, so the open chat is taken from the
// refreshed list and the audited detail read only runs when it is not on this page.
// A row from a list read that a later read superseded (the one act() starts, or a
// newer version bump's) may be stale, so it is never copied; the detail read runs instead.
watch(() => chatStore.adminSummary?.version, async (version, old) => {
  if (version === undefined || old === undefined || version === old) return
  const sequence = detailSequence
  const fresh = await loadList()
  const id = selectedId.value
  if (!id) return
  void thread.poll()
  // An action or another detail read wrote detail meanwhile.
  if (sequence !== detailSequence) return
  const row = fresh ? chats.value.find((chat) => chat.id === id) : undefined
  if (row) {
    // Invalidate an older detail read still in flight.
    detailSequence++
    detail.value = row
  } else {
    void loadDetail()
  }
})
watch(selectedId, openThread)
watch(canView, () => { void loadList() })

onMounted(() => {
  if (typeof window.matchMedia === 'function') {
    media = window.matchMedia(WIDE_QUERY)
    onMedia()
    media.addEventListener?.('change', onMedia)
  }
  clock = setInterval(() => { now.value = Date.now() }, 30_000)
  releaseFast = watchStaffSummaryFast()
  document.addEventListener('visibilitychange', onVisibility)
  void loadList()
  openThread(selectedId.value)
})

onBeforeUnmount(() => {
  if (keywordTimer) clearTimeout(keywordTimer)
  if (clock) clearInterval(clock)
  media?.removeEventListener?.('change', onMedia)
  releaseFast?.()
  document.removeEventListener('visibilitychange', onVisibility)
  thread.reset()
})
</script>
