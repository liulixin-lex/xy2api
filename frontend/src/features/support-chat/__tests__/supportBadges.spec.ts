import { describe, expect, it } from 'vitest'
import { useSupportChatStore, useSupportTicketStore } from '@/stores'
import { useSupportBadges } from '../useSupportBadges'

describe('support badge totals', () => {
  it('adds ticket and chat counts for users and staff', () => {
    const tickets = useSupportTicketStore()
    const chat = useSupportChatStore()
    const { userBadge, staffBadge } = useSupportBadges()
    tickets.setUserUnread(2)
    chat.userChatUnread = 3
    expect(userBadge.value).toBe(5)
    tickets.setAdminPending(4)
    chat.setAdminSummary({ chat_waiting: 6, ticket_pending: 4, latest_user_message_id: 10, version: 1 })
    expect(staffBadge.value).toBe(10)
  })

  it('does not lose the chat count when only the ticket count is written', () => {
    const tickets = useSupportTicketStore()
    const chat = useSupportChatStore()
    const { staffBadge, userBadge } = useSupportBadges()
    chat.setAdminSummary({ chat_waiting: 7, ticket_pending: 1, latest_user_message_id: 3, version: 2 })
    tickets.setAdminPending(1)
    // e.g. the ticket list page refreshes its own count
    tickets.setAdminPending(0)
    expect(tickets.adminPending).toBe(0)
    expect(staffBadge.value).toBe(7)
    chat.userChatUnread = 2
    tickets.setUserUnread(0)
    expect(userBadge.value).toBe(2)
  })
})
