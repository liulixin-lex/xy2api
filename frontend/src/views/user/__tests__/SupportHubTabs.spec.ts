import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import UserSupportView from '../SupportTicketsView.vue'
import AdminSupportView from '../../admin/SupportTicketsView.vue'

const mocks = vi.hoisted(() => ({
  replace: vi.fn(),
  route: null as null | { query: Record<string, string> },
  app: { cachedPublicSettings: {} as Record<string, unknown> },
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('vue-router', async () => ({
  ...(await vi.importActual<typeof import('vue-router')>('vue-router')),
  useRoute: () => mocks.route,
  useRouter: () => ({ replace: mocks.replace }),
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks.app }))

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  MyTicketList: { template: '<div data-testid="my-ticket-list" />' },
  SupportChatConversation: { props: ['active'], template: '<div data-testid="conversation" :data-active="active" />' },
  AdminTicketList: { template: '<div data-testid="admin-ticket-list"><slot name="actions" /></div>' },
  AdminSupportChatInbox: { template: '<div data-testid="chat-inbox"><slot name="header" /></div>' },
  SupportAlertToggles: { template: '<div data-testid="alert-toggles" />' },
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.route = reactive({ query: {} })
  mocks.app.cachedPublicSettings = { support_ticket_enabled: true, support_chat_enabled: true }
})

describe('客户服务 page tabs (user)', () => {
  it('opens the chat by default and switches with ?tab', async () => {
    const wrapper = mount(UserSupportView, { global: { stubs } })
    expect(wrapper.get('[data-testid="support-tab-chat"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.get('[data-testid="conversation"]').attributes('data-active')).toBe('true')
    await wrapper.get('[data-testid="support-tab-tickets"]').trigger('click')
    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'tickets' } })
    mocks.route!.query = { tab: 'tickets' }
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="conversation"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="my-ticket-list"]').exists()).toBe(true)
  })

  it('shows only the tickets without the chat switch', () => {
    mocks.app.cachedPublicSettings = { support_ticket_enabled: true, support_chat_enabled: false }
    mocks.route!.query = { tab: 'chat' }
    const wrapper = mount(UserSupportView, { global: { stubs } })
    expect(wrapper.find('[role="tablist"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="my-ticket-list"]').exists()).toBe(true)
  })
})

describe('客户服务 page tabs (admin)', () => {
  it('opens the inbox by default with the alert toggles, and the tickets with ?tab=tickets', async () => {
    const wrapper = mount(AdminSupportView, { global: { stubs } })
    expect(wrapper.find('[data-testid="chat-inbox"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="alert-toggles"]').exists()).toBe(true)
    await wrapper.get('[data-testid="support-tab-tickets"]').trigger('click')
    expect(mocks.replace).toHaveBeenCalledWith({ query: { tab: 'tickets' } })
    mocks.route!.query = { tab: 'tickets' }
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[data-testid="admin-ticket-list"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="support-tab-tickets"]').attributes('aria-selected')).toBe('true')
  })

  it('keeps the plain ticket table without the chat switch', () => {
    mocks.app.cachedPublicSettings = { support_ticket_enabled: true, support_chat_enabled: false }
    const wrapper = mount(AdminSupportView, { global: { stubs } })
    expect(wrapper.find('[data-testid="chat-inbox"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="admin-ticket-list"]').exists()).toBe(true)
    expect(wrapper.find('[role="tablist"]').exists()).toBe(false)
  })
})
