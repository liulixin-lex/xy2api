import { describe, expect, it, vi } from 'vitest'
import { routeHidesSupportWidget, shouldShowSupportWidget, type SupportWidgetVisibilityInput } from '../supportWidgetVisibility'

interface RouteRecordLike {
  path: string
  meta?: Record<string, unknown>
}

const harness = vi.hoisted(() => ({ routes: [] as RouteRecordLike[] }))

vi.mock('vue-router', () => ({
  createWebHistory: vi.fn(() => ({})),
  createRouter: vi.fn((options: { routes: RouteRecordLike[] }) => {
    harness.routes = options.routes
    return { beforeEach: vi.fn(), afterEach: vi.fn(), onError: vi.fn() }
  }),
}))

const visible: SupportWidgetVisibilityInput = { chatEnabled: true, canAccessAdmin: false, backendMode: false, mobileOpen: false, desktop: false, hiddenByRoute: false }

describe('support widget visibility', () => {
  it('shows only when every condition holds', () => {
    expect(shouldShowSupportWidget(visible)).toBe(true)
    const blockers: Array<Partial<SupportWidgetVisibilityInput>> = [
      { chatEnabled: false },
      { canAccessAdmin: true },
      { backendMode: true },
      { mobileOpen: true },
      { hiddenByRoute: true },
    ]
    for (const blocker of blockers) expect(shouldShowSupportWidget({ ...visible, ...blocker }), JSON.stringify(blocker)).toBe(false)
    // Every combination with at least one blocker stays hidden.
    for (let mask = 1; mask < 32; mask++) {
      const input = { ...visible }
      blockers.forEach((blocker, index) => { if (mask & (1 << index)) Object.assign(input, blocker) })
      expect(shouldShowSupportWidget(input)).toBe(false)
    }
  })

  it('counts the mobile drawer only below lg, so a stale mobileOpen cannot hide the bubble on desktop', () => {
    expect(shouldShowSupportWidget({ ...visible, mobileOpen: true, desktop: false })).toBe(false)
    expect(shouldShowSupportWidget({ ...visible, mobileOpen: true, desktop: true })).toBe(true)
    expect(shouldShowSupportWidget({ ...visible, mobileOpen: false, desktop: true })).toBe(true)
    expect(shouldShowSupportWidget({ ...visible, mobileOpen: true, desktop: true, hiddenByRoute: true })).toBe(false)
  })

  it.each([
    ['/login', true], ['/register', true], ['/email-verify', true], ['/forgot-password', true], ['/reset-password', true],
    ['/auth/callback', true], ['/auth/linuxdo/callback', true], ['/auth/dingtalk/email-completion', true],
    ['/payment/qrcode', true], ['/payment/stripe', true], ['/payment/airwallex', true], ['/payment/stripe-popup', true],
    ['/payment/result', false],
    ['/setup', true], ['/support-tickets', true], ['/support-tickets/:id(\\d+)', true],
    ['/admin', true], ['/admin/dashboard', true], ['/admin/support-tickets', true],
    ['/home', false], ['/dashboard', false], ['/keys', false], ['/purchase', false], ['/model-plaza', false], ['/custom/:id', false],
    ['/administrator', false], ['/support-ticketsx', false],
  ])('route %s hides the bubble: %s', (path, hidden) => {
    expect(routeHidesSupportWidget(path)).toBe(hidden)
  })

  it('marks the real routes with meta.hideSupportWidget', async () => {
    await import('@/router')
    const meta = (path: string) => harness.routes.find((route) => route.path === path)?.meta?.hideSupportWidget
    for (const path of ['/login', '/register', '/email-verify', '/forgot-password', '/reset-password', '/auth/callback', '/auth/oidc/callback',
      '/payment/qrcode', '/payment/stripe', '/setup', '/support-tickets', '/support-tickets/:id(\\d+)', '/admin/dashboard', '/admin/support-tickets', '/admin/settings']) {
      expect(meta(path), path).toBe(true)
    }
    for (const path of ['/home', '/dashboard', '/keys', '/payment/result', '/profile', '/usage']) {
      expect(meta(path), path).toBeUndefined()
    }
  })
})
