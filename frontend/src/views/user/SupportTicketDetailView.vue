<template>
  <AppLayout>
    <div class="mx-auto max-w-4xl space-y-4">
      <router-link
        to="/support-tickets"
        class="inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-white"
      >
        <Icon name="arrowLeft" size="sm" />
        {{ t('supportTickets.detail.back') }}
      </router-link>

      <div v-if="loading && !detail" class="card h-64 animate-pulse" aria-busy="true"></div>
      <p v-else-if="loadError" class="card p-6 text-sm text-red-600 dark:text-red-400" data-testid="ticket-load-error">{{ loadError }}</p>
      <template v-else-if="detail">
        <div class="card p-5">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0">
              <h2 class="break-words text-lg font-semibold text-gray-900 dark:text-white" data-testid="ticket-title">{{ detail.ticket.title }}</h2>
              <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                <span>{{ t('supportTickets.detail.number', { id: detail.ticket.id }) }}</span>
                <span v-if="detail.ticket.category" class="rounded-md bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700">{{ detail.ticket.category }}</span>
                <span>{{ t('supportTickets.detail.createdAt', { time: formatDateTimeToMinute(detail.ticket.created_at) }) }}</span>
              </p>
            </div>
            <div class="flex items-center gap-2">
              <TicketStatusBadge :status="detail.ticket.status" />
              <button
                v-if="!closed"
                type="button"
                class="btn btn-secondary btn-sm"
                :disabled="busy"
                data-testid="ticket-close"
                @click="confirmClose = true"
              >
                {{ t('supportTickets.detail.close') }}
              </button>
            </div>
          </div>
        </div>

        <div class="card p-5">
          <TicketConversation :messages="detail.messages" viewer="user" />
        </div>

        <div v-if="closed" class="card flex flex-wrap items-center justify-between gap-3 p-5 text-sm text-gray-600 dark:text-gray-300" data-testid="ticket-closed">
          <span>
            {{ detail.ticket.closed_by_role === 'admin' ? t('supportTickets.detail.closedBySupport') : t('supportTickets.detail.closedByYou') }}
            {{ t('supportTickets.detail.closedHintUser') }}
          </span>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="ticket-reopen" @click="reopen">
            {{ t('supportTickets.detail.reopen') }}
          </button>
        </div>
        <div v-else class="card p-5">
          <TicketReplyBox v-model="reply" :placeholder="t('supportTickets.detail.replyPlaceholder')" :sending="busy" @submit="send" />
        </div>
        <p class="text-center text-xs text-gray-400">{{ t('supportTickets.detail.autoRefresh') }}</p>
      </template>
    </div>

    <ConfirmDialog
      :show="confirmClose"
      :title="t('supportTickets.detail.close')"
      :message="t('supportTickets.detail.closeConfirm')"
      :confirm-text="t('supportTickets.detail.close')"
      danger
      @confirm="close"
      @cancel="confirmClose = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import TicketConversation from '@/features/support-tickets/TicketConversation.vue'
import TicketReplyBox from '@/features/support-tickets/TicketReplyBox.vue'
import TicketStatusBadge from '@/features/support-tickets/TicketStatusBadge.vue'
import { useSupportTicketRequests } from '@/features/support-tickets/useSupportTicketRequests'
import { supportTicketErrorMessage } from '@/features/support-tickets/supportTickets'
import { closeMyTicket, getMyTicket, reopenMyTicket, replyMyTicket, type SupportTicketDetail } from '@/api/supportTickets'
import { useAppStore } from '@/stores/app'
import { useSupportTicketStore } from '@/stores/supportTickets'
import { formatDateTimeToMinute } from '@/utils/format'

const POLL_MS = 30_000

const { t } = useI18n()
const route = useRoute()
const appStore = useAppStore()
const ticketStore = useSupportTicketStore()

const detail = ref<SupportTicketDetail | null>(null)
const loadError = ref('')
const reply = ref('')
const confirmClose = ref(false)
let timer: ReturnType<typeof setInterval> | undefined

const ticketId = computed(() => Number(route.params.id))
const closed = computed(() => detail.value?.ticket.status === 'closed')
const requests = useSupportTicketRequests(() => ticketId.value, () => {
  detail.value = null
  loadError.value = ''
  reply.value = ''
  confirmClose.value = false
})
const { loading, busy } = requests

/** quiet reloads (polling) keep the page as it is when they fail. */
async function load(quiet = false) {
  const request = requests.begin()
  if (!request) return
  const id = ticketId.value
  if (!quiet) {
    loading.value = true
    loadError.value = ''
  }
  try {
    const result = await getMyTicket(id)
    if (!request.current()) return
    detail.value = result
    if (!quiet) ticketStore.refresh(true)
  } catch (err) {
    if (request.current() && !quiet) loadError.value = supportTicketErrorMessage(err, t, 'supportTickets.loadFailed')
  } finally {
    request.finish()
  }
}

async function run(action: () => Promise<SupportTicketDetail>, successKey: string) {
  const request = requests.begin(true)
  if (!request) return false
  try {
    const result = await action()
    if (!request.current()) return false
    detail.value = result
    appStore.showSuccess(t(successKey))
    ticketStore.refresh(true)
    return request
  } catch (err) {
    if (request.current()) appStore.showError(supportTicketErrorMessage(err, t))
    return false
  } finally {
    request.finish()
  }
}

async function send(body: string) {
  const result = await run(() => replyMyTicket(ticketId.value, body), 'supportTickets.detail.sent')
  if (result && result.current()) reply.value = ''
}

async function close() {
  confirmClose.value = false
  await run(() => closeMyTicket(ticketId.value), 'supportTickets.detail.closed')
}

function reopen() {
  return run(() => reopenMyTicket(ticketId.value), 'supportTickets.detail.reopened')
}

function poll() {
  if (document.visibilityState === 'visible' && !busy.value && detail.value) load(true)
}

watch(ticketId, () => { void load() })

onMounted(() => {
  load()
  timer = setInterval(poll, POLL_MS)
})

onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>
