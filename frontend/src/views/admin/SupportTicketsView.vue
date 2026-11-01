<template>
  <AppLayout>
    <AdminSupportChatInbox v-if="tab === 'chats'">
      <template #header>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <SupportTabs :model-value="tab" :tabs="tabs" :label="t('supportTickets.title')" @update:model-value="setTab" />
          <SupportAlertToggles />
        </div>
      </template>
    </AdminSupportChatInbox>
    <AdminTicketList v-else>
      <template v-if="chatEnabled" #actions>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <SupportTabs :model-value="tab" :tabs="tabs" :label="t('supportTickets.title')" @update:model-value="setTab" />
          <SupportAlertToggles />
        </div>
      </template>
    </AdminTicketList>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import AdminTicketList from '@/features/support-tickets/AdminTicketList.vue'
import AdminSupportChatInbox from '@/features/support-chat/AdminSupportChatInbox.vue'
import SupportAlertToggles from '@/features/support-chat/SupportAlertToggles.vue'
import SupportTabs from '@/features/support-chat/SupportTabs.vue'
import { useSupportChatStore, useSupportTicketStore } from '@/stores'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'

type HubTab = 'chats' | 'tickets'

// 客户服务 for staff: the chat inbox and the ticket table, switched by ?tab=chats|tickets.
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const chatStore = useSupportChatStore()
const ticketStore = useSupportTicketStore()

const chatEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.supportChat))
const tab = computed<HubTab>(() => {
  if (!chatEnabled.value) return 'tickets'
  return route.query?.tab === 'tickets' ? 'tickets' : 'chats'
})
const tabs = computed<Array<{ value: HubTab; label: string; badge?: number }>>(() => [
  { value: 'chats', label: t('supportChat.tabs.chats'), badge: chatStore.adminChatWaiting },
  { value: 'tickets', label: t('supportChat.tabs.tickets'), badge: ticketStore.adminPending },
])

function setTab(value: string) {
  if (value === tab.value) return
  void router.replace({ query: { tab: value === 'tickets' ? 'tickets' : 'chats' } })
}
</script>
