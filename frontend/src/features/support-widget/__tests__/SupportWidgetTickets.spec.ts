import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SupportWidgetTickets from '../SupportWidgetTickets.vue'
import { useSupportTicketStore } from '@/stores/supportTickets'

const mocks = vi.hoisted(() => ({ listMyTickets: vi.fn(), getMySummary: vi.fn(), push: vi.fn() }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('vue-router', async () => ({
  ...(await vi.importActual<typeof import('vue-router')>('vue-router')),
  useRouter: () => ({ push: mocks.push }),
}))
vi.mock('@/api/supportTickets', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportTickets')>()),
  listMyTickets: mocks.listMyTickets, getMySummary: mocks.getMySummary,
}))

const stubs = {
  Icon: true,
  RouterLink: { props: ['to'], emits: ['click'], template: '<a :href="to" @click.prevent="$emit(\'click\', $event)"><slot /></a>' },
  CreateTicketForm: { props: ['summary', 'active'], emits: ['cancel', 'created'], template: '<form data-testid="ticket-create-form" />' },
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getMySummary.mockResolvedValue({ unread_count: 1, open_count: 0, max_open: 5, categories: [], notice: '' })
  mocks.listMyTickets.mockResolvedValue({
    items: [{ id: 3, title: '401', status: 'replied', user_unread: true, last_message_at: '2026-10-31T00:00:00Z' }],
    total: 1, page: 1, page_size: 5, pages: 1,
  })
})

describe('panel tickets tab', () => {
  it('lists recent tickets that open the full detail page', async () => {
    const wrapper = mount(SupportWidgetTickets, { props: { active: true }, global: { stubs } })
    await flushPromises()
    expect(mocks.listMyTickets).toHaveBeenCalledWith({ status: '', page: 1, page_size: 5 })
    expect(useSupportTicketStore().userUnread).toBe(1)
    const row = wrapper.get('[data-testid="widget-ticket-row"]')
    expect(row.attributes('href')).toBe('/support-tickets/3')
    await row.trigger('click')
    expect(wrapper.emitted('navigate')).toHaveLength(1)
  })

  it('embeds the new-ticket form and goes to the created ticket', async () => {
    const wrapper = mount(SupportWidgetTickets, { props: { active: true }, global: { stubs } })
    await flushPromises()
    await wrapper.get('[data-testid="widget-ticket-new"]').trigger('click')
    const form = wrapper.findComponent(stubs.CreateTicketForm)
    expect(form.props('summary')).toMatchObject({ max_open: 5 })
    form.vm.$emit('created', { ticket: { id: 9 }, messages: [] })
    await flushPromises()
    expect(mocks.push).toHaveBeenCalledWith('/support-tickets/9')
    expect(wrapper.emitted('navigate')).toHaveLength(1)
  })
})
