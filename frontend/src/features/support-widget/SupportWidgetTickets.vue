<template>
  <div class="min-h-0 flex-1 overflow-y-auto p-4" data-testid="widget-tickets">
    <CreateTicketForm
      v-if="view === 'form'"
      :summary="summary"
      :active="view === 'form'"
      compact
      @cancel="view = 'list'"
      @created="onCreated"
    />
    <template v-else>
      <div class="mb-3 flex items-center justify-between gap-2">
        <router-link to="/support-tickets?tab=tickets" class="text-sm text-primary-600 hover:underline dark:text-primary-400" @click="emit('navigate')">
          {{ t('supportChat.widget.allTickets') }}
        </router-link>
        <button type="button" class="btn btn-primary btn-sm" data-testid="widget-ticket-new" @click="view = 'form'">
          <Icon name="plus" size="sm" class="mr-1" />
          {{ t('supportTickets.newTicket') }}
        </button>
      </div>
      <div v-if="loading && !tickets.length" class="space-y-2" aria-busy="true">
        <div v-for="index in 3" :key="index" class="h-14 animate-pulse rounded-xl bg-gray-100 dark:bg-dark-800"></div>
      </div>
      <p v-else-if="loadError" class="py-6 text-center text-sm text-red-600 dark:text-red-400">{{ loadError }}</p>
      <p v-else-if="!tickets.length" class="py-6 text-center text-sm text-gray-500 dark:text-gray-400" data-testid="widget-ticket-empty">
        {{ t('supportTickets.empty') }}
      </p>
      <ul v-else class="space-y-2">
        <li v-for="ticket in tickets" :key="ticket.id">
          <router-link
            :to="`/support-tickets/${ticket.id}`"
            class="block rounded-xl px-3 py-2 ring-1 ring-gray-200 transition-colors hover:bg-gray-50 dark:ring-dark-700 dark:hover:bg-dark-800"
            data-testid="widget-ticket-row"
            @click="emit('navigate')"
          >
            <div class="flex items-center gap-2">
              <span v-if="ticket.user_unread" class="h-2 w-2 shrink-0 rounded-full bg-red-500" :title="t('supportTickets.unread')"></span>
              <span class="min-w-0 flex-1 truncate text-sm font-medium text-gray-900 dark:text-white">{{ ticket.title }}</span>
              <TicketStatusBadge :status="ticket.status" />
            </div>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">#{{ ticket.id }} · {{ formatRelativeTime(ticket.last_message_at) }}</p>
          </router-link>
        </li>
      </ul>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import CreateTicketForm from '@/features/support-tickets/CreateTicketForm.vue'
import TicketStatusBadge from '@/features/support-tickets/TicketStatusBadge.vue'
import { supportTicketErrorMessage } from '@/features/support-tickets/supportTickets'
import {
  getMySummary,
  listMyTickets,
  type SupportTicket,
  type SupportTicketDetail,
  type SupportTicketUserSummary,
} from '@/api/supportTickets'
import { useSupportTicketStore } from '@/stores/supportTickets'
import { formatRelativeTime } from '@/utils/format'

const PAGE_SIZE = 5

// Panel 工单 tab: a short list and the new-ticket form; full details open on the 客户服务 page.
const props = defineProps<{ active: boolean }>()
const emit = defineEmits<{ (e: 'navigate'): void }>()
const { t } = useI18n()
const router = useRouter()
const ticketStore = useSupportTicketStore()

const view = ref<'list' | 'form'>('list')
const tickets = ref<SupportTicket[]>([])
const summary = ref<SupportTicketUserSummary | null>(null)
const loading = ref(false)
const loadError = ref('')
let sequence = 0

async function load() {
  const current = ++sequence
  loading.value = true
  loadError.value = ''
  try {
    const [list, mine] = await Promise.all([
      listMyTickets({ status: '', page: 1, page_size: PAGE_SIZE }),
      getMySummary(),
    ])
    if (current !== sequence) return
    tickets.value = list.items
    summary.value = mine
    ticketStore.setUserUnread(mine.unread_count)
  } catch (err) {
    if (current === sequence) loadError.value = supportTicketErrorMessage(err, t, 'supportTickets.loadFailed')
  } finally {
    if (current === sequence) loading.value = false
  }
}

function onCreated(detail: SupportTicketDetail) {
  view.value = 'list'
  emit('navigate')
  void router.push(`/support-tickets/${detail.ticket.id}`)
}

watch(() => props.active, (active) => {
  if (active) void load()
  else view.value = 'list'
}, { immediate: true })
</script>
