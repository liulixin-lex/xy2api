<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl space-y-4">
      <SupportTabs
        v-if="chatEnabled"
        :model-value="tab"
        :tabs="tabs"
        :label="t('supportTickets.title')"
        @update:model-value="setTab"
      />
      <div
        v-if="tab === 'chat'"
        class="card flex h-[calc(100dvh-12rem)] min-h-[26rem] flex-col overflow-hidden"
        data-testid="support-chat-tab"
      >
        <SupportChatConversation :active="tab === 'chat'" />
      </div>
      <MyTicketList v-else />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import MyTicketList from '@/features/support-tickets/MyTicketList.vue'
import SupportChatConversation from '@/features/support-chat/SupportChatConversation.vue'
import SupportTabs from '@/features/support-chat/SupportTabs.vue'
import { useSupportChatStore, useSupportTicketStore } from '@/stores'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'

type HubTab = 'chat' | 'tickets'

// 客户服务: the user's chat with support and their tickets, sharing data with the floating widget.
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const chatStore = useSupportChatStore()
const ticketStore = useSupportTicketStore()

const chatEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.supportChat))
const tab = computed<HubTab>(() => {
  if (!chatEnabled.value) return 'tickets'
  return route.query?.tab === 'tickets' ? 'tickets' : 'chat'
})
const tabs = computed<Array<{ value: HubTab; label: string; badge?: number }>>(() => [
  { value: 'chat', label: t('supportChat.tabs.chat'), badge: chatStore.userChatUnread },
  { value: 'tickets', label: t('supportChat.tabs.tickets'), badge: ticketStore.userUnread },
])

function setTab(value: string) {
  if (value === tab.value) return
  void router.replace({ query: { ...route.query, tab: value === 'tickets' ? 'tickets' : 'chat' } })
}
</script>
