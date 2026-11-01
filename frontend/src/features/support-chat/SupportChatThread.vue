<template>
  <div ref="scroller" class="min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 py-4" data-testid="chat-thread" @scroll.passive="onScroll">
    <div v-if="state.loadingOlder" class="flex justify-center pb-3 text-gray-400" aria-busy="true">
      <Icon name="refresh" size="sm" class="animate-spin" />
    </div>
    <p
      v-if="welcome && !state.hasOlder"
      class="mb-4 whitespace-pre-wrap rounded-2xl bg-primary-50 px-4 py-3 text-sm leading-relaxed text-primary-900 dark:bg-primary-900/30 dark:text-primary-100"
      data-testid="chat-welcome"
    >{{ welcome }}</p>

    <div v-if="state.loading && !state.loaded" class="space-y-3" aria-busy="true">
      <div v-for="index in 3" :key="index" class="h-10 animate-pulse rounded-2xl bg-gray-100 dark:bg-dark-800" :class="index % 2 ? 'mr-12' : 'ml-12'"></div>
    </div>
    <p v-else-if="state.error && !state.loaded" class="py-6 text-center text-sm text-red-600 dark:text-red-400" data-testid="chat-load-error">
      {{ errorText }}
      <button type="button" class="ml-1 text-primary-600 hover:underline dark:text-primary-400" @click="thread.load()">{{ t('supportChat.retry') }}</button>
    </p>
    <p
      v-else-if="state.loaded && !state.messages.length && !state.pending.length && !welcome"
      class="py-8 text-center text-sm text-gray-500 dark:text-gray-400"
      data-testid="chat-empty"
    >{{ emptyText || t('supportChat.empty') }}</p>

    <ol class="space-y-3">
      <li
        v-for="message in state.messages"
        :key="message.id"
        class="flex"
        :class="rowClass(message)"
        data-testid="chat-message"
        :data-role="message.author_role"
        :data-id="message.id"
      >
        <p v-if="message.author_role === 'system'" class="max-w-[90%] text-center text-xs text-gray-500 dark:text-gray-400" data-testid="chat-system">
          {{ message.body }}
        </p>
        <div v-else class="group min-w-0 max-w-[85%]">
          <div class="mb-1 flex items-center gap-2 text-[11px] text-gray-500 dark:text-gray-400" :class="isMine(message) ? 'justify-end' : ''">
            <span class="font-medium text-gray-700 dark:text-gray-200" data-testid="chat-message-author">{{ authorLabel(message) }}</span>
            <time :datetime="message.created_at">{{ formatDateTimeToMinute(message.created_at) }}</time>
            <button
              v-if="recallable(message)"
              type="button"
              class="rounded px-1 font-medium text-gray-500 opacity-100 hover:text-red-600 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 [@media(hover:none)]:opacity-100 dark:text-gray-400"
              :title="t('supportChat.recallMessage')"
              data-testid="chat-message-recall"
              @click="emit('recall', message)"
            >
              {{ t('supportChat.recall') }}
            </button>
            <button
              v-else-if="canDelete && !message.deleted && message.author_role === 'admin'"
              type="button"
              class="rounded px-1 font-medium text-gray-500 opacity-100 hover:text-red-600 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 [@media(hover:none)]:opacity-100 dark:text-gray-400"
              :title="t('supportChat.admin.recallMessage')"
              data-testid="chat-message-recall"
              @click="emit('delete', message)"
            >
              {{ t('supportChat.admin.recall') }}
            </button>
            <button
              v-else-if="canDelete && !message.deleted"
              type="button"
              class="rounded p-0.5 text-gray-400 opacity-100 hover:text-red-600 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 [@media(hover:none)]:opacity-100"
              :title="t('supportChat.admin.deleteMessage')"
              :aria-label="t('supportChat.admin.deleteMessage')"
              data-testid="chat-message-delete"
              @click="emit('delete', message)"
            >
              <Icon name="trash" size="xs" />
            </button>
          </div>
          <div
            v-if="message.deleted"
            class="rounded-2xl border border-dashed border-gray-200 px-3 py-2 text-sm italic text-gray-400 dark:border-dark-600 dark:text-gray-500"
            data-testid="chat-message-deleted"
            :data-recalled="message.recalled ? 'true' : undefined"
          >{{ deletedText(message) }}</div>
          <div v-else class="space-y-2 rounded-2xl px-3 py-2" :class="bubbleClass(message)">
            <SupportChatImage v-if="message.image" :image="message.image" :scope="imageScope" />
            <TicketMessageBody v-if="message.body" :text="message.body" />
          </div>
          <p
            v-if="showReceipt(message)"
            class="mt-1 flex items-center justify-end gap-0.5 text-[11px]"
            :class="receiptClass(message)"
            data-testid="chat-receipt"
            :data-state="isRead(message) ? 'read' : 'unread'"
          >
            <Icon v-if="isRead(message)" name="check" size="xs" />
            {{ receiptText(message) }}
          </p>
        </div>
      </li>
      <li v-for="item in state.pending" :key="item.client_msg_id" class="flex justify-end" data-testid="chat-pending" :data-state="item.state">
        <div class="min-w-0 max-w-[85%]">
          <div class="space-y-2 rounded-2xl rounded-tr-md bg-primary-50 px-3 py-2 text-gray-900 dark:bg-primary-900/30 dark:text-gray-100" :class="item.state === 'sending' ? 'opacity-70' : ''">
            <SupportChatImage
              v-if="item.image"
              :image="null"
              :scope="imageScope"
              :local-url="item.previewUrl"
              :local-width="item.image.width"
              :local-height="item.image.height"
            />
            <TicketMessageBody v-if="item.body" :text="item.body" />
          </div>
          <p v-if="item.state === 'sending'" class="mt-1 text-right text-[11px] text-gray-400">{{ t('supportChat.sending') }}</p>
          <p v-else class="mt-1 flex flex-wrap items-center justify-end gap-2 text-[11px]">
            <span class="text-red-600 dark:text-red-400" data-testid="chat-pending-error">{{ pendingError(item) }}</span>
            <button type="button" class="font-medium text-primary-600 hover:underline dark:text-primary-400" data-testid="chat-retry" @click="thread.retry(item.client_msg_id)">
              {{ t('supportChat.resend') }}
            </button>
            <button type="button" class="text-gray-500 hover:underline dark:text-gray-400" data-testid="chat-discard" @click="thread.discard(item.client_msg_id)">
              {{ t('supportChat.discard') }}
            </button>
          </p>
        </div>
      </li>
    </ol>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import TicketMessageBody from '@/features/support-tickets/TicketMessageBody.vue'
import { SUPPORT_CHAT_RECALL_WINDOW_MS, type SupportChatMessage } from '@/api/supportChat'
import type { SupportChatImageScope } from '@/stores/supportChat'
import { formatDateTimeToMinute } from '@/utils/format'
import type { ChatThread, PendingChatMessage } from './chatThread'
import SupportChatImage from './SupportChatImage.vue'
import { supportChatErrorMessage } from './supportChat'

const LOAD_OLDER_THRESHOLD = 48
const STICK_TO_BOTTOM_THRESHOLD = 120

// viewer decides which side is "mine" and how authors are named; users never see which staff member answered.
const props = defineProps<{
  thread: ChatThread
  viewer: 'user' | 'admin'
  imageScope: SupportChatImageScope
  welcome?: string
  emptyText?: string
  canDelete?: boolean
  /** The viewer may recall their own messages within the recall window (users). */
  canRecall?: boolean
  /** Messages the server already refused to recall (too old). */
  recallExpiredIds?: ReadonlySet<number>
}>()
const emit = defineEmits<{ (e: 'delete', message: SupportChatMessage): void; (e: 'recall', message: SupportChatMessage): void }>()
const { t } = useI18n()

const scroller = ref<HTMLElement | null>(null)
const state = computed(() => props.thread.state)
const errorText = computed(() => supportChatErrorMessage(state.value.error, t, 'supportChat.loadFailed'))
function isMine(message: SupportChatMessage) {
  return props.viewer === 'user' ? message.author_role === 'user' : message.author_role === 'admin'
}

function isRead(message: SupportChatMessage) {
  return message.id <= state.value.peerReadId
}

// Only staff see whether their messages were read; users never see read state.
function showReceipt(message: SupportChatMessage) {
  return props.viewer === 'admin' && isMine(message) && !message.deleted
}

function receiptText(message: SupportChatMessage) {
  return isRead(message) ? t('supportChat.read') : t('supportChat.unread')
}

function receiptClass(message: SupportChatMessage) {
  return isRead(message) ? 'text-primary-600 dark:text-primary-400' : 'text-gray-400'
}

// A recalled message was taken back by its own side; otherwise staff removed a user's message.
function deletedText(message: SupportChatMessage) {
  if (!message.recalled) return t('supportChat.deleted')
  if (message.author_role !== 'user') return t('supportChat.recalledByStaff')
  return props.viewer === 'user' ? t('supportChat.recalledBySelf') : t('supportChat.recalledByUser')
}

// The recall button shows while the message is young enough by the server clock;
// the server has the final say.
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined
function recallable(message: SupportChatMessage) {
  if (!props.canRecall || message.deleted || !isMine(message) || props.recallExpiredIds?.has(message.id)) return false
  return now.value - state.value.clockSkewMs - Date.parse(message.created_at) < SUPPORT_CHAT_RECALL_WINDOW_MS
}

// Users see only the staff account id, never a staff name or email.
function staffLabel(message: SupportChatMessage) {
  return message.staff_id ? t('supportChat.staffWithId', { id: message.staff_id }) : t('supportChat.staff')
}

function rowClass(message: SupportChatMessage) {
  if (message.author_role === 'system') return 'justify-center'
  return isMine(message) ? 'justify-end' : 'justify-start'
}

function bubbleClass(message: SupportChatMessage) {
  return isMine(message)
    ? 'rounded-tr-md bg-primary-50 text-gray-900 dark:bg-primary-900/30 dark:text-gray-100'
    : 'rounded-tl-md bg-gray-50 text-gray-900 ring-1 ring-gray-200 dark:bg-dark-800 dark:text-gray-100 dark:ring-dark-700'
}

function authorLabel(message: SupportChatMessage) {
  if (props.viewer === 'user') {
    if (message.author_role === 'user') return t('supportChat.you')
    return message.author_role === 'admin' ? staffLabel(message) : t('supportChat.staff')
  }
  if (message.author_role === 'admin') return message.author_name || staffLabel(message)
  if (message.author_role === 'bot') return t('supportChat.bot')
  return t('supportChat.user')
}

function pendingError(item: PendingChatMessage) {
  return supportChatErrorMessage(item.error, t, 'supportChat.sendFailed')
}

function nearBottom() {
  const el = scroller.value
  return !el || el.scrollHeight - el.scrollTop - el.clientHeight < STICK_TO_BOTTOM_THRESHOLD
}

function scrollToBottom() {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

async function onScroll() {
  const el = scroller.value
  if (!el || el.scrollTop > LOAD_OLDER_THRESHOLD || !state.value.hasOlder || state.value.loadingOlder) return
  // Keep the visible message in place while older ones are inserted above it.
  const before = el.scrollHeight
  await props.thread.loadOlder()
  await nextTick()
  el.scrollTop += el.scrollHeight - before
}

// Follow new messages when the reader is already at the bottom, or when they sent one.
watch(
  () => [state.value.messages.length ? state.value.messages[state.value.messages.length - 1].id : 0, state.value.pending.length] as const,
  async ([lastId, pending], old) => {
    const [oldLastId, oldPending] = old ?? [0, 0]
    const grew = lastId > oldLastId || pending > oldPending
    if (!grew) return
    const follow = nearBottom() || pending > oldPending || oldLastId === 0
    await nextTick()
    if (follow) scrollToBottom()
  },
)

onMounted(() => {
  void nextTick(scrollToBottom)
  if (props.canRecall) clock = setInterval(() => (now.value = Date.now()), 10_000)
})
onBeforeUnmount(() => {
  if (clock) clearInterval(clock)
})
</script>
