import { computed } from 'vue'
import { useSupportChatStore, useSupportTicketStore } from '@/stores'

/**
 * Badge totals for 客户服务. Each store keeps its own meaning (tickets only,
 * chat only); only these sums are shown on the sidebar, the bubble and the title.
 */
export function useSupportBadges() {
  const tickets = useSupportTicketStore()
  const chat = useSupportChatStore()
  return {
    userBadge: computed(() => tickets.userUnread + chat.userChatUnread),
    staffBadge: computed(() => tickets.adminPending + chat.adminChatWaiting),
  }
}
