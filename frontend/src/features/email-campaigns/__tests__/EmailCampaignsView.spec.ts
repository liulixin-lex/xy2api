import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import EmailCampaignsView from '../EmailCampaignsView.vue'
import type { Campaign } from '../types'

enableAutoUnmount(afterEach)

const api = vi.hoisted(() => ({
  MAX_PAGE_SIZE: 100,
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  previewAudience: vi.fn(),
  renderContent: vi.fn(),
  sendTest: vi.fn(),
  createCampaign: vi.fn(),
  listCampaigns: vi.fn(),
  getCampaign: vi.fn(),
  listRecipients: vi.fn(),
  pauseCampaign: vi.fn(),
  resumeCampaign: vi.fn(),
  cancelCampaign: vi.fn(),
  retryFailed: vi.fn(),
  listSuppressions: vi.fn(),
  addSuppressions: vi.fn(),
  removeSuppression: vi.fn(),
}))

const stepUpRun = vi.hoisted(() => vi.fn((action: () => Promise<unknown>) => action()))

vi.mock('../api', () => api)

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: stepUpRun, visible: ref(false) }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => '',
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { email: 'admin@example.com' } }),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

function campaign(overrides: Partial<Campaign> = {}): Campaign {
  return {
    id: 7,
    subject: 'Spring sale',
    status: 'completed',
    pause_reason: '',
    last_error: '',
    consecutive_failures: 0,
    waiting_for_limit: '',
    audience: {
      registered_from: null,
      registered_to: null,
      timezone: 'UTC',
      inactive_days: null,
      exclude_user_ids: [],
      skipped: { no_valid_email: 0, suppressed: 0, excluded: 0, duplicate: 0 },
    },
    counts: { total: 10, pending: 0, sending: 0, sent: 10, failed: 0, skipped: 0, uncertain: 0 },
    created_by: { id: 1, email: 'admin@example.com' },
    created_at: '2026-11-10T00:00:00Z',
    started_at: '2026-11-10T00:00:00Z',
    finished_at: null,
    updated_at: '2026-11-10T00:00:00Z',
    ...overrides,
  }
}

function page<T>(items: T[]) {
  return { items, total: items.length, page: 1, page_size: 20, pages: 1 }
}

function mountView() {
  return mount(EmailCampaignsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        DataTable: {
          props: ['data'],
          template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>',
        },
        Pagination: true,
        Icon: true,
        TotpStepUpDialog: true,
      },
    },
  })
}

async function openComposeWithContent(wrapper: ReturnType<typeof mountView>) {
  await wrapper.get('[data-testid="email-campaign-new"]').trigger('click')
  await flushPromises()
  await wrapper.get('[data-testid="email-campaign-subject"]').setValue('Hello')
  await wrapper.get('[data-testid="email-campaign-body"]').setValue('Hi {{username}}')
  await flushPromises()
}

function confirmButton(wrapper: ReturnType<typeof mountView>) {
  const button = wrapper.findAll('button').find((b) => b.text() === 'admin.emailCampaigns.compose.confirmStart')
  if (!button) throw new Error('confirm button not rendered')
  return button
}

describe('EmailCampaignsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    stepUpRun.mockImplementation((action: () => Promise<unknown>) => action())
    api.getSettings.mockResolvedValue({
      per_minute: 20,
      per_day: 500,
      smtp_configured: true,
      site_url: 'https://example.com',
      site_url_ok: true,
      sent_last_minute: 0,
      sent_last_24h: 0,
    })
    api.listCampaigns.mockResolvedValue(page([campaign()]))
    api.previewAudience.mockResolvedValue({
      ...page([]),
      matched: 42,
      skipped: { no_valid_email: 1, suppressed: 2, excluded: 0, duplicate: 0 },
      exclude_resolved: 0,
      exclude_not_found: [],
    })
    api.renderContent.mockResolvedValue({ subject: 'Hello', html: '<p>Hi</p>', text: 'Hi' })
    api.sendTest.mockResolvedValue({ to: 'admin@example.com' })
    api.createCampaign.mockResolvedValue(campaign({ id: 8, status: 'sending' }))
    api.getCampaign.mockResolvedValue({ ...campaign({ id: 8 }), body_markdown: '', preview_html: '' })
    api.listRecipients.mockResolvedValue(page([]))
  })

  it('starts a campaign through step-up and warns when no test email was sent', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openComposeWithContent(wrapper)

    await wrapper.get('[data-testid="email-campaign-send"]').trigger('click')
    expect(wrapper.find('[data-testid="email-campaign-no-test-warning"]').exists()).toBe(true)

    await confirmButton(wrapper).trigger('click')
    await flushPromises()

    expect(stepUpRun).toHaveBeenCalledTimes(1)
    expect(api.createCampaign).toHaveBeenCalledTimes(1)
    expect(api.createCampaign).toHaveBeenCalledWith({
      subject: 'Hello',
      body_markdown: 'Hi {{username}}',
      audience: expect.objectContaining({ timezone: expect.any(String) }),
    })
  })

  it('hides the warning after a test email in the same dialog session', async () => {
    const wrapper = mountView()
    await flushPromises()
    await openComposeWithContent(wrapper)

    await wrapper.get('[data-testid="email-campaign-test-send"]').trigger('click')
    await flushPromises()
    expect(api.sendTest).toHaveBeenCalledWith({ subject: 'Hello', body_markdown: 'Hi {{username}}', to: 'admin@example.com' })

    await wrapper.get('[data-testid="email-campaign-send"]').trigger('click')
    expect(wrapper.find('[data-testid="email-campaign-no-test-warning"]').exists()).toBe(false)
  })

  it('pauses a sending campaign directly, without step-up', async () => {
    api.listCampaigns.mockResolvedValue(page([campaign({ status: 'sending' })]))
    api.pauseCampaign.mockResolvedValue(campaign({ status: 'paused' }))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="email-campaign-new"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="email-campaign-pause"]').trigger('click')
    await flushPromises()

    expect(api.pauseCampaign).toHaveBeenCalledWith(7)
    expect(stepUpRun).not.toHaveBeenCalled()
  })
})
