import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import SupportWidget from '../SupportWidget.vue'
import { BUBBLE_STORAGE_KEY } from '../snapBubble'
import { SUPPORT_WIDGET_REOPEN_KEY } from '../supportWidgetVisibility'

const api = vi.hoisted(() => ({
  getMyChat: vi.fn(), listMyChatMessages: vi.fn(), sendMyChatMessage: vi.fn(), markMyChatRead: vi.fn(), fetchMyChatImage: vi.fn(),
  getAdminChatSummary: vi.fn(), listAdminChats: vi.fn(), getAdminChat: vi.fn(), listAdminChatMessages: vi.fn(),
}))
const state = vi.hoisted(() => ({
  auth: null as null | { isAuthenticated: boolean; canAccessAdmin: boolean; user: { id: number } | null },
  app: null as null | { contactInfo: string; sidebarCollapsed: boolean; sidebarLayouts: number; cachedPublicSettings: Record<string, unknown> },
  route: { fullPath: '/pricing?plan=pro', meta: { requiresAuth: false } as Record<string, unknown> },
  motion: { value: false },
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('vue-router', async () => ({
  ...(await vi.importActual<typeof import('vue-router')>('vue-router')),
  useRoute: () => state.route,
}))
vi.mock('@/api/supportChat', async (importOriginal) => ({ ...(await importOriginal<typeof import('@/api/supportChat')>()), ...api }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
// Read once when the bubble mounts; set it before mounting.
vi.mock('@/composables/useMotionPreference', () => ({ useMotionPreference: () => ({ reducedMotion: state.motion }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => state.app }))

const stubs = {
  Icon: true,
  RouterLink: { props: ['to'], emits: ['click'], template: '<a :href="to" @click.prevent="$emit(\'click\', $event)"><slot /></a>' },
  SupportChatConversation: { props: ['active'], template: '<div data-testid="conversation-stub" :data-active="active" />' },
  SupportWidgetTickets: { props: ['active'], template: '<div data-testid="tickets-stub" />' },
  Transition: { template: '<slot />' },
}

function setAuth(authenticated: boolean) {
  state.auth = reactive({ isAuthenticated: authenticated, canAccessAdmin: false, user: authenticated ? { id: 5 } : null })
}

async function click(element: Element, detail: number) {
  element.dispatchEvent(new MouseEvent('click', { detail, bubbles: true, cancelable: true }))
  await flushPromises()
}

function mountWidget() {
  return mount(SupportWidget, { global: { stubs }, attachTo: document.body })
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  sessionStorage.clear()
  document.body.className = ''
  setAuth(false)
  state.route.meta = { requiresAuth: false }
  state.motion.value = false
  state.app = reactive({ contactInfo: '微信：xy2-support\nQQ 群：123', sidebarCollapsed: false, sidebarLayouts: 0, cachedPublicSettings: { support_chat_enabled: true, support_ticket_enabled: true } })
  api.getMyChat.mockResolvedValue({ chat: null, unread_count: 1, ticket_unread_count: 2, welcome: 'hi' })
})

afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

describe('support widget for guests', () => {
  it('shows the login prompt and never calls a support chat endpoint', async () => {
    vi.useFakeTimers()
    const wrapper = mountWidget()
    const bubble = wrapper.get('[data-testid="support-bubble"]')
    expect(bubble.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-testid="support-bubble-badge"]').exists()).toBe(false)
    await bubble.trigger('click')
    expect(bubble.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('[data-testid="support-guest-contact"]').text()).toContain('微信：xy2-support')
    expect(wrapper.find('[data-testid="support-panel-full"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="conversation-stub"]').exists()).toBe(false)

    const login = wrapper.get('[data-testid="support-guest-login"]')
    expect(login.attributes('href')).toBe(`/login?redirect=${encodeURIComponent('/pricing?plan=pro')}`)
    expect(wrapper.get('[data-testid="support-guest-register"]').attributes('href')).toBe('/register')
    await login.trigger('click')
    expect(sessionStorage.getItem(SUPPORT_WIDGET_REOPEN_KEY)).toBe('1')

    await vi.advanceTimersByTimeAsync(5 * 60_000)
    for (const fn of Object.values(api)) expect(fn).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('support widget for users', () => {
  it('reopens after login, reads the summary and shows the combined badge', async () => {
    setAuth(true)
    sessionStorage.setItem(SUPPORT_WIDGET_REOPEN_KEY, '1')
    const wrapper = mountWidget()
    await flushPromises()
    expect(api.getMyChat).toHaveBeenCalledTimes(1)
    expect(sessionStorage.getItem(SUPPORT_WIDGET_REOPEN_KEY)).toBeNull()
    expect(wrapper.get('[data-testid="support-panel"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="conversation-stub"]').attributes('data-active')).toBe('true')
    expect(wrapper.get('[data-testid="support-bubble-badge"]').text()).toBe('3')
    expect(wrapper.get('[data-testid="support-panel-full"]').attributes('href')).toBe('/support-tickets?tab=chat')
    wrapper.unmount()
  })

  it('polls the summary every 60s while collapsed, 15s after the user sent', async () => {
    vi.useFakeTimers()
    setAuth(true)
    const wrapper = mountWidget()
    await flushPromises()
    expect(api.getMyChat).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(59_000)
    expect(api.getMyChat).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(api.getMyChat).toHaveBeenCalledTimes(2)
    const { useSupportChatStore } = await import('@/stores/supportChat')
    useSupportChatStore().lastSentAt = Date.now()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getMyChat).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(15_000)
    expect(api.getMyChat).toHaveBeenCalledTimes(4)
    wrapper.unmount()
  })

  it('shows the GG mark while collapsed and a close icon while open', async () => {
    setAuth(true)
    const wrapper = mountWidget()
    const bubble = wrapper.get('[data-testid="support-bubble"]')
    expect(wrapper.get('[data-testid="support-bubble-mark"]').text()).toBe('GG')
    expect(wrapper.get('[data-testid="support-bubble-mark"]').attributes('aria-hidden')).toBe('true')
    expect(bubble.attributes('aria-expanded')).toBe('false')
    await bubble.trigger('click')
    expect(wrapper.find('[data-testid="support-bubble-mark"]').exists()).toBe(false)
    expect(bubble.get('icon-stub').attributes('name')).toBe('x')
    expect(bubble.attributes('aria-expanded')).toBe('true')
    wrapper.unmount()
  })

  it('closes on Esc unless a dialog is open or the key was handled', async () => {
    setAuth(true)
    const wrapper = mountWidget()
    await wrapper.get('[data-testid="support-bubble"]').trigger('click')
    const panel = wrapper.get('[data-testid="support-panel"]')
    document.body.classList.add('modal-open')
    await panel.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="support-panel"]').exists()).toBe(true)
    document.body.classList.remove('modal-open')
    const handled = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    handled.preventDefault()
    panel.element.dispatchEvent(handled)
    await flushPromises()
    expect(wrapper.find('[data-testid="support-panel"]').exists()).toBe(true)
    await panel.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="support-panel"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('ignores the Esc that cancels an IME composition', async () => {
    setAuth(true)
    const wrapper = mountWidget()
    await wrapper.get('[data-testid="support-bubble"]').trigger('click')
    const panel = wrapper.get('[data-testid="support-panel"]')
    // macOS Chrome/Edge: isComposing; Safari: composition already ended but keyCode 229.
    panel.element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true, cancelable: true }))
    await flushPromises()
    expect(wrapper.find('[data-testid="support-panel"]').exists()).toBe(true)
    panel.element.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', keyCode: 229, bubbles: true, cancelable: true }))
    await flushPromises()
    expect(wrapper.find('[data-testid="support-panel"]').exists()).toBe(true)
    await panel.trigger('keydown', { key: 'Escape' })
    expect(wrapper.find('[data-testid="support-panel"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('stops the summary timer for good when unmounted during a summary read', async () => {
    vi.useFakeTimers()
    setAuth(true)
    const wrapper = mountWidget()
    await flushPromises()
    expect(api.getMyChat).toHaveBeenCalledTimes(1)
    let resolve!: (value: unknown) => void
    api.getMyChat.mockReturnValueOnce(new Promise((r) => { resolve = r }))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getMyChat).toHaveBeenCalledTimes(2)
    wrapper.unmount()
    resolve({ chat: null, unread_count: 0, ticket_unread_count: 0, welcome: 'hi' })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(5 * 60_000)
    expect(api.getMyChat).toHaveBeenCalledTimes(2)
  })
})

describe('support bubble left inset', () => {
  let width: number
  beforeEach(() => {
    width = window.innerWidth
    Object.defineProperty(window, 'innerWidth', { value: 1280, configurable: true, writable: true })
    localStorage.setItem(BUBBLE_STORAGE_KEY, JSON.stringify({ side: 'left', topRatio: 1 }))
  })
  afterEach(() => {
    Object.defineProperty(window, 'innerWidth', { value: width, configurable: true, writable: true })
  })
  const x = (wrapper: ReturnType<typeof mountWidget>) =>
    Number(/translate3d\((-?\d+)px/.exec(wrapper.get('[data-testid="support-bubble"]').attributes('style') ?? '')?.[1])

  it('sits right of the sidebar whenever a layout renders it, whatever the route meta says', async () => {
    setAuth(true)
    state.route.meta = { requiresAuth: false } // e.g. /model-plaza?embedded=1 inside AppLayout
    state.app!.sidebarLayouts = 1
    const wrapper = mountWidget()
    expect(x(wrapper)).toBe(272)
    state.app!.sidebarCollapsed = true
    await flushPromises()
    expect(x(wrapper)).toBe(88)
    wrapper.unmount()
  })

  it('uses the plain edge without a rendered sidebar, also for signed-in users', async () => {
    setAuth(true)
    state.route.meta = {} // e.g. the 404 page: protected, no AppLayout
    const wrapper = mountWidget()
    expect(x(wrapper)).toBe(16)
    state.app!.sidebarLayouts = 1
    Object.defineProperty(window, 'innerWidth', { value: 900, configurable: true, writable: true })
    window.dispatchEvent(new Event('resize'))
    await flushPromises()
    expect(x(wrapper)).toBe(16) // below lg the sidebar is a drawer
    wrapper.unmount()
  })
})

describe('support bubble drag', () => {
  it('swallows the click that ends a drag, snaps to the nearer edge and remembers it', async () => {
    const wrapper = mountWidget()
    const bubble = wrapper.get('[data-testid="support-bubble"]')
    const pointer = { button: 0, isPrimary: true, pointerId: 1 }
    await bubble.trigger('pointerdown', { ...pointer, clientX: 900, clientY: 600 })
    await bubble.trigger('pointermove', { ...pointer, clientX: 902, clientY: 601 }) // under the 4px threshold
    await bubble.trigger('pointerup', { ...pointer, clientX: 902, clientY: 601 })
    await click(bubble.element, 1)
    expect(bubble.attributes('aria-expanded')).toBe('true')
    await click(bubble.element, 1)

    await bubble.trigger('pointerdown', { ...pointer, clientX: 900, clientY: 600 })
    await bubble.trigger('pointermove', { ...pointer, clientX: 100, clientY: 300 })
    expect(bubble.attributes('style')).toContain('transition: none')
    await bubble.trigger('pointerup', { ...pointer, clientX: 100, clientY: 300 })
    await click(bubble.element, 1)
    expect(bubble.attributes('aria-expanded')).toBe('false')
    expect(JSON.parse(localStorage.getItem(BUBBLE_STORAGE_KEY)!)).toMatchObject({ side: 'left' })
    expect(bubble.attributes('style')).toContain('transform 200ms')
    // Keyboard activation (detail 0) is never swallowed.
    await click(bubble.element, 0)
    expect(bubble.attributes('aria-expanded')).toBe('true')
    wrapper.unmount()
  })

  it('has no transition when reduced motion is on', () => {
    state.motion.value = true
    const wrapper = mountWidget()
    expect(wrapper.get('[data-testid="support-bubble"]').attributes('style')).toContain('transition: none')
    wrapper.unmount()
  })
})
