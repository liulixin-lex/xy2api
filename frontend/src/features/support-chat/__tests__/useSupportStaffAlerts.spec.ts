import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { useSupportChatStore, useSupportTicketStore } from '@/stores'
import {
  STAFF_SUMMARY_HIDDEN_MS,
  STAFF_SUMMARY_INBOX_MS,
  STAFF_SUMMARY_VISIBLE_MS,
  useSupportAlertToggles,
  useSupportStaffAlerts,
  watchStaffSummaryFast,
} from '../useSupportStaffAlerts'

const mocks = vi.hoisted(() => ({
  getAdminChatSummary: vi.fn(), getAdminSummary: vi.fn(), push: vi.fn(), warning: vi.fn(),
  auth: { isAuthenticated: true, canAccessAdmin: true, isAdmin: true, user: { id: 1, role: 'admin' } as { id: number; role: string } | null },
  app: { cachedPublicSettings: { support_chat_enabled: true, support_ticket_enabled: true } as Record<string, unknown> },
  can: vi.fn(() => true),
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('vue-router', async () => ({
  ...(await vi.importActual<typeof import('vue-router')>('vue-router')),
  useRouter: () => ({ push: mocks.push }),
}))
vi.mock('@/api/supportChat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportChat')>()),
  getAdminChatSummary: mocks.getAdminChatSummary,
}))
vi.mock('@/api/supportTickets', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/supportTickets')>()),
  getAdminSummary: mocks.getAdminSummary,
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ ...mocks.app, showWarning: mocks.warning }) }))
vi.mock('@/stores/adminAccess', () => ({ useAdminAccessStore: () => ({ can: mocks.can, ensureLoaded: vi.fn() }) }))

const summary = (latest: number, extra: Record<string, number> = {}) => ({ chat_waiting: 2, ticket_pending: 3, latest_user_message_id: latest, version: latest, ...extra })

class FakeNotification {
  static permission: NotificationPermission = 'granted'
  static requestPermission = vi.fn(async () => FakeNotification.permission)
  static instances: FakeNotification[] = []
  onclick: (() => void) | null = null
  close = vi.fn()
  constructor(public title: string, public options: NotificationOptions) {
    FakeNotification.instances.push(this)
  }
}

let visibility: DocumentVisibilityState = 'visible'

function mountAlerts() {
  return mount(defineComponent({ setup() { useSupportStaffAlerts(); return () => h('div') } }))
}

function mountToggles() {
  let api!: ReturnType<typeof useSupportAlertToggles>
  mount(defineComponent({ setup() { api = useSupportAlertToggles(); return () => h('div') } }))
  return api
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
  localStorage.clear()
  visibility = 'visible'
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
  FakeNotification.instances = []
  FakeNotification.permission = 'granted'
  vi.stubGlobal('Notification', FakeNotification)
  mocks.auth.user = { id: 1, role: 'admin' }
  mocks.can.mockReturnValue(true)
  mocks.app.cachedPublicSettings = { support_chat_enabled: true, support_ticket_enabled: true }
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function enableNotifications() {
  const toggles = mountToggles()
  if (!toggles.notifyEnabled.value) await toggles.toggleNotify()
  expect(toggles.notifyEnabled.value).toBe(true)
  return toggles
}

describe('staff support alerts', () => {
  it('polls only the unaudited summary and writes both counts', async () => {
    mocks.getAdminChatSummary.mockResolvedValue(summary(10))
    const wrapper = mountAlerts()
    await flushPromises()
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(1)
    expect(useSupportChatStore().adminChatWaiting).toBe(2)
    expect(useSupportTicketStore().adminPending).toBe(3)

    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_VISIBLE_MS)
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(2)
    visibility = 'hidden'
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_VISIBLE_MS)
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_HIDDEN_MS - STAFF_SUMMARY_VISIBLE_MS)
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(3)

    visibility = 'visible'
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(4)
    const release = watchStaffSummaryFast()
    await flushPromises()
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(5)
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_INBOX_MS)
    expect(mocks.getAdminChatSummary).toHaveBeenCalledTimes(6)
    release()
    expect(mocks.getAdminSummary).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('alerts on every new user message after the first poll, also when counts go down and up', async () => {
    await enableNotifications()
    mocks.getAdminChatSummary
      .mockResolvedValueOnce(summary(10))
      .mockResolvedValueOnce(summary(11)) // follow-up in an already waiting chat: count unchanged
      .mockResolvedValueOnce(summary(11))
      .mockResolvedValueOnce(summary(13, { chat_waiting: 2 })) // one answered, another arrived
    const wrapper = mountAlerts()
    await flushPromises()
    expect(FakeNotification.instances).toHaveLength(0) // baseline
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_VISIBLE_MS)
    expect(FakeNotification.instances).toHaveLength(1)
    expect(FakeNotification.instances[0].options).toMatchObject({ tag: 'xy2-support-chat' })
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_VISIBLE_MS)
    expect(FakeNotification.instances).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_VISIBLE_MS)
    expect(FakeNotification.instances).toHaveLength(2)
    expect(localStorage.getItem('support_alert_last_id:1')).toBe('13')

    const focus = vi.spyOn(window, 'focus').mockImplementation(() => undefined)
    FakeNotification.instances[1].onclick?.()
    expect(focus).toHaveBeenCalled()
    expect(mocks.push).toHaveBeenCalledWith('/admin/support-tickets?tab=chats')
    wrapper.unmount()
  })

  it('does not alert twice across tabs or after a reload', async () => {
    await enableNotifications()
    mocks.getAdminChatSummary.mockResolvedValueOnce(summary(10)).mockResolvedValueOnce(summary(12))
    const wrapper = mountAlerts()
    await flushPromises()
    // Another tab already alerted for id 12.
    localStorage.setItem('support_alert_last_id:1', '12')
    window.dispatchEvent(new StorageEvent('storage', { key: 'support_alert_last_id:1', newValue: '12' }))
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_VISIBLE_MS)
    expect(FakeNotification.instances).toHaveLength(0)
    wrapper.unmount()

    // A reload: the first poll is only a baseline.
    mocks.getAdminChatSummary.mockResolvedValueOnce(summary(15))
    const reloaded = mountAlerts()
    await flushPromises()
    expect(FakeNotification.instances).toHaveLength(0)
    reloaded.unmount()
  })

  it('stays silent and makes no request without view permission or with chat off', async () => {
    mocks.can.mockReturnValue(false)
    const wrapper = mountAlerts()
    await flushPromises()
    mocks.can.mockReturnValue(true)
    mocks.app.cachedPublicSettings = { support_ticket_enabled: true, support_chat_enabled: false }
    const other = mountAlerts()
    await vi.advanceTimersByTimeAsync(STAFF_SUMMARY_HIDDEN_MS)
    expect(mocks.getAdminChatSummary).not.toHaveBeenCalled()
    wrapper.unmount()
    other.unmount()
  })
})

describe('alert toggles', () => {
  it('turns desktop alerts off again with a toast when permission is denied', async () => {
    FakeNotification.permission = 'default'
    FakeNotification.requestPermission.mockResolvedValueOnce('denied')
    const toggles = mountToggles()
    if (toggles.notifyEnabled.value) await toggles.toggleNotify()
    await toggles.toggleNotify()
    expect(FakeNotification.requestPermission).toHaveBeenCalled()
    expect(toggles.notifyEnabled.value).toBe(false)
    expect(localStorage.getItem('support_alert_notify')).toBe('0')
    expect(mocks.warning).toHaveBeenCalledWith('supportChat.alerts.notifyDenied')
  })

  it('persists the sound switch and asks for a click while audio is locked', async () => {
    const resume = vi.fn(async () => undefined)
    class FakeAudioContext { state = 'suspended'; resume = resume }
    vi.stubGlobal('AudioContext', FakeAudioContext)
    const toggles = mountToggles()
    if (toggles.soundEnabled.value) toggles.toggleSound()
    toggles.toggleSound()
    expect(localStorage.getItem('support_alert_sound')).toBe('1')
    expect(resume).toHaveBeenCalled()
    expect(toggles.soundSuspended.value).toBe(true)
    toggles.toggleSound()
    expect(localStorage.getItem('support_alert_sound')).toBe('0')
    expect(toggles.soundSuspended.value).toBe(false)
  })
})
