import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { useAppStore } from '@/stores/app'
import AppLayout from '../AppLayout.vue'

vi.mock('@/composables/useOnboardingTour', () => ({ useOnboardingTour: () => ({ replayTour: vi.fn() }) }))
vi.mock('@/composables/usePageEntrance', () => ({ usePageEntrance: vi.fn() }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ setReplayCallback: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ canAccessAdmin: false }) }))

const stubs = { AppSidebar: true, AppHeader: true, CommandPalette: true }

describe('AppLayout sidebar flag', () => {
  it('counts mounted layouts so the support bubble knows the sidebar is shown', () => {
    const app = useAppStore()
    expect(app.sidebarLayouts).toBe(0)
    const first = mount(AppLayout, { global: { stubs } })
    // Route change: the next view's layout mounts before the old one unmounts.
    const second = mount(AppLayout, { global: { stubs } })
    expect(app.sidebarLayouts).toBe(2)
    first.unmount()
    expect(app.sidebarLayouts).toBe(1)
    second.unmount()
    expect(app.sidebarLayouts).toBe(0)
    app.unregisterSidebarLayout()
    expect(app.sidebarLayouts).toBe(0)
  })
})
