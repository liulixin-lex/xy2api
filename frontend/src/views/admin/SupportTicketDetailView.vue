<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-4">
      <router-link
        to="/admin/support-tickets?tab=tickets"
        class="inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-white"
      >
        <Icon name="arrowLeft" size="sm" />
        {{ t('supportTickets.detail.back') }}
      </router-link>

      <div v-if="loading && !detail" class="card h-64 animate-pulse" aria-busy="true"></div>
      <p v-else-if="loadError" class="card p-6 text-sm text-red-600 dark:text-red-400" data-testid="ticket-load-error">{{ loadError }}</p>
      <div v-else-if="detail" class="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div class="min-w-0 space-y-4">
          <div class="card p-5">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0">
                <h2 class="break-words text-lg font-semibold text-gray-900 dark:text-white" data-testid="ticket-title">{{ detail.ticket.title }}</h2>
                <p class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                  <span>{{ t('supportTickets.detail.number', { id: detail.ticket.id }) }}</span>
                  <span class="rounded-md bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700">{{ detail.ticket.category || t('supportTickets.noCategory') }}</span>
                  <span>{{ t('supportTickets.detail.createdAt', { time: formatDateTimeToMinute(detail.ticket.created_at) }) }}</span>
                </p>
              </div>
              <TicketStatusBadge :status="detail.ticket.status" />
            </div>
          </div>

          <div class="card p-5">
            <TicketConversation :messages="detail.messages" viewer="admin" />
          </div>

          <div v-if="closed" class="card flex flex-wrap items-center justify-between gap-3 p-5 text-sm text-gray-600 dark:text-gray-300" data-testid="ticket-closed">
            <span>{{ detail.ticket.closed_by_role === 'user' ? t('supportTickets.admin.closedByUser') : t('supportTickets.admin.closedByAdmin') }}</span>
            <button v-if="canManage" type="button" class="btn btn-secondary btn-sm" :disabled="busy" data-testid="ticket-reopen" @click="setStatus('pending')">
              {{ t('supportTickets.admin.reopen') }}
            </button>
          </div>
          <div v-else-if="canManage" class="card p-5">
            <TicketReplyBox v-model="reply" :placeholder="t('supportTickets.detail.adminReplyPlaceholder')" :sending="busy" @submit="send">
              <template #extra>
                <label class="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
                  <span class="whitespace-nowrap">{{ t('supportTickets.admin.replyStatus') }}</span>
                  <Select v-model="replyStatus" :options="replyStatusOptions" class="w-52" data-testid="ticket-reply-status" />
                </label>
              </template>
            </TicketReplyBox>
          </div>
          <p class="text-center text-xs text-gray-400">{{ t('supportTickets.detail.autoRefresh') }}</p>
        </div>

        <aside class="space-y-4">
          <AdminSupportUserCard :user="user" :user-id="detail.ticket.user_id" />

          <div v-if="canManage" class="card space-y-2 p-5">
            <button
              v-if="canManage && !closed && detail.ticket.status !== 'processing'"
              type="button"
              class="btn btn-secondary w-full"
              :disabled="busy"
              data-testid="ticket-mark-processing"
              @click="setStatus('processing')"
            >
              {{ t('supportTickets.admin.markProcessing') }}
            </button>
            <button
              v-if="canManage && !closed"
              type="button"
              class="btn btn-secondary w-full"
              :disabled="busy"
              data-testid="ticket-close"
              @click="setStatus('closed')"
            >
              {{ t('supportTickets.admin.close') }}
            </button>
            <button v-if="canManage" type="button" class="btn btn-danger w-full" :disabled="busy" data-testid="ticket-delete" @click="confirmDelete = true">
              {{ t('supportTickets.admin.delete') }}
            </button>
          </div>
        </aside>
      </div>
    </div>

    <ConfirmDialog
      :show="confirmDelete"
      :title="t('supportTickets.admin.deleteTitle')"
      :message="t('supportTickets.admin.deleteConfirm', { id: detail?.ticket.id ?? '', title: detail?.ticket.title ?? '' })"
      :confirm-text="t('supportTickets.admin.delete')"
      danger
      @confirm="remove"
      @cancel="confirmDelete = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import AdminSupportUserCard from '@/features/support-tickets/AdminSupportUserCard.vue'
import TicketConversation from '@/features/support-tickets/TicketConversation.vue'
import TicketReplyBox from '@/features/support-tickets/TicketReplyBox.vue'
import TicketStatusBadge from '@/features/support-tickets/TicketStatusBadge.vue'
import { useSupportTicketRequests } from '@/features/support-tickets/useSupportTicketRequests'
import { supportTicketErrorMessage } from '@/features/support-tickets/supportTickets'
import {
  deleteTicket,
  getTicket,
  replyTicket,
  setTicketStatus,
  type SupportTicketDetail,
  type SupportTicketReplyStatus,
} from '@/api/supportTickets'
import { useAdminPermission } from '@/composables/useAdminPermission'
import { useAppStore } from '@/stores/app'
import { useSupportTicketStore } from '@/stores/supportTickets'
import { formatDateTimeToMinute } from '@/utils/format'

const POLL_MS = 30_000

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const ticketStore = useSupportTicketStore()
const { canView, canManage } = useAdminPermission('support_tickets')

const detail = ref<SupportTicketDetail | null>(null)
const loadError = ref('')
const reply = ref('')
const replyStatus = ref<SupportTicketReplyStatus>('replied')
const confirmDelete = ref(false)
let timer: ReturnType<typeof setInterval> | undefined

const ticketId = computed(() => Number(route.params.id))
const closed = computed(() => detail.value?.ticket.status === 'closed')
const requests = useSupportTicketRequests(() => ticketId.value, () => {
  detail.value = null
  loadError.value = ''
  reply.value = ''
  replyStatus.value = 'replied'
  confirmDelete.value = false
}, () => canView.value)
const { loading, busy } = requests
const user = computed(() => detail.value?.ticket.user)
const replyStatusOptions = computed(() => [
  { value: 'replied', label: t('supportTickets.admin.replyStatusOptions.replied') },
  { value: 'processing', label: t('supportTickets.admin.replyStatusOptions.processing') },
  { value: 'closed', label: t('supportTickets.admin.replyStatusOptions.closed') },
])

async function load(quiet = false) {
  const request = requests.begin()
  if (!request) return
  const id = ticketId.value
  if (!quiet) {
    loading.value = true
    loadError.value = ''
  }
  try {
    const result = await getTicket(id)
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
  if (!canManage.value) return false
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
  const result = await run(() => replyTicket(ticketId.value, body, replyStatus.value), 'supportTickets.detail.sent')
  if (result && result.current()) {
    reply.value = ''
    replyStatus.value = 'replied'
  }
}

function setStatus(status: 'processing' | 'closed' | 'pending') {
  return run(() => setTicketStatus(ticketId.value, status), 'supportTickets.admin.statusUpdated')
}

async function remove() {
  if (!canManage.value) return
  const request = requests.begin(true)
  if (!request) return
  confirmDelete.value = false
  const id = ticketId.value
  try {
    await deleteTicket(id)
    if (!request.current()) return
    appStore.showSuccess(t('supportTickets.admin.deleted'))
    ticketStore.refresh(true)
    router.push('/admin/support-tickets?tab=tickets')
  } catch (err) {
    if (request.current()) appStore.showError(supportTicketErrorMessage(err, t))
  } finally {
    request.finish()
  }
}

function poll() {
  if (document.visibilityState === 'visible' && !busy.value && detail.value) load(true)
}

watch(canManage, () => { requests.reset(); void load() })
watch([ticketId, canView], () => { void load() })

onMounted(() => {
  load()
  timer = setInterval(poll, POLL_MS)
})

onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
})
</script>
