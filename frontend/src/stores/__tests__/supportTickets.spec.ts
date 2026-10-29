import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useSupportTicketStore } from '../supportTickets'

const mocks = vi.hoisted(() => ({
  getMySummary: vi.fn(),
  getAdminSummary: vi.fn(),
  can: vi.fn(), ensureLoaded: vi.fn(),
  auth: { isAuthenticated: true, isAdmin: false, canAccessAdmin: false },
  app: { cachedPublicSettings: { support_ticket_enabled: true } as Record<string, unknown> | null },
}))
vi.mock('@/api/supportTickets', () => ({ getMySummary: mocks.getMySummary, getAdminSummary: mocks.getAdminSummary }))
vi.mock('../auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('../adminAccess', () => ({ useAdminAccessStore: () => ({ can: mocks.can, ensureLoaded: mocks.ensureLoaded }) }))
vi.mock('../app', () => ({ useAppStore: () => mocks.app }))

beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
  vi.useFakeTimers()
  mocks.can.mockReturnValue(false)
  mocks.ensureLoaded.mockResolvedValue(undefined)
  mocks.auth.isAuthenticated = true
  mocks.auth.isAdmin = false
  mocks.auth.canAccessAdmin = false
  mocks.app.cachedPublicSettings = { support_ticket_enabled: true }
  mocks.getMySummary.mockResolvedValue({ unread_count: 3 })
  mocks.getAdminSummary.mockResolvedValue({ pending_count: 8 })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('support ticket badge store', () => {
  it('reads the user summary at most once a minute unless forced', async () => {
    const store = useSupportTicketStore()
    await store.refresh()
    expect(store.userUnread).toBe(3)
    await store.refresh()
    expect(mocks.getMySummary).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(60_000)
    await store.refresh()
    expect(mocks.getMySummary).toHaveBeenCalledTimes(2)
    await store.refresh(true)
    expect(mocks.getMySummary).toHaveBeenCalledTimes(3)
    expect(mocks.getAdminSummary).not.toHaveBeenCalled()
  })

  it('reads the admin queue for admins', async () => {
    mocks.auth.isAdmin = true
    const store = useSupportTicketStore()
    await store.refresh()
    expect(store.adminPending).toBe(8)
    expect(mocks.getMySummary).not.toHaveBeenCalled()
  })

  it('skips operators, who have no admin ticket menu', async () => {
    mocks.auth.canAccessAdmin = true
    const store = useSupportTicketStore()
    await store.refresh(true)
    expect(mocks.getMySummary).not.toHaveBeenCalled()
    expect(mocks.getAdminSummary).not.toHaveBeenCalled()
  })

  it('stays at zero without a session or while the feature is off', async () => {
    const store = useSupportTicketStore()
    store.setUserUnread(2)
    mocks.app.cachedPublicSettings = { support_ticket_enabled: false }
    await store.refresh(true)
    expect(store.userUnread).toBe(0)
    mocks.app.cachedPublicSettings = null
    await store.refresh(true)
    mocks.app.cachedPublicSettings = { support_ticket_enabled: true }
    mocks.auth.isAuthenticated = false
    await store.refresh(true)
    expect(mocks.getMySummary).not.toHaveBeenCalled()
  })

  it('retries on the next navigation after a failure and resets on logout', async () => {
    mocks.getMySummary.mockRejectedValueOnce(new Error('offline'))
    const store = useSupportTicketStore()
    await store.refresh()
    await store.refresh()
    expect(mocks.getMySummary).toHaveBeenCalledTimes(2)
    expect(store.userUnread).toBe(3)
    store.setAdminPending(-1)
    expect(store.adminPending).toBe(0)
    store.reset()
    expect(store.userUnread).toBe(0)
    await store.refresh()
    expect(mocks.getMySummary).toHaveBeenCalledTimes(3)
  })
  it.each(['user', 'admin'] as const)('drops a late %s summary after the feature is disabled', async role => {
    let resolve!: (value: { unread_count: number; pending_count: number }) => void
    const pending = new Promise<{ unread_count: number; pending_count: number }>(ok => { resolve = ok })
    mocks.auth.isAdmin = role === 'admin'
    const summary = role === 'admin' ? mocks.getAdminSummary : mocks.getMySummary
    summary.mockReturnValueOnce(pending)
    const store = useSupportTicketStore()
    const request = store.refresh(true)
    mocks.app.cachedPublicSettings = { support_ticket_enabled: false }
    await store.refresh(true)
    resolve({ unread_count: 99, pending_count: 99 })
    await request
    expect(store.userUnread).toBe(0)
    expect(store.adminPending).toBe(0)
  })

  it.each(['view', 'manage'])('reads only the admin summary for an authorized %s operator', async () => {
    mocks.auth.canAccessAdmin = true
    mocks.can.mockReturnValue(true)
    const store = useSupportTicketStore()
    await store.refresh(true)
    expect(mocks.can).toHaveBeenCalledWith('support_tickets:view')
    expect(mocks.getAdminSummary).toHaveBeenCalledOnce()
    expect(mocks.getMySummary).not.toHaveBeenCalled()
    expect(store.adminPending).toBe(8)
  })

  it('does not issue a summary when access loading finishes after logout', async () => {
    let resolve!: () => void
    mocks.auth.canAccessAdmin = true
    mocks.can.mockReturnValue(true)
    mocks.ensureLoaded.mockReturnValueOnce(new Promise<void>(ok => { resolve = ok }))
    const store = useSupportTicketStore()
    const request = store.refresh(true)
    mocks.auth.isAuthenticated = false
    await store.refresh(true)
    resolve()
    await request
    expect(mocks.getMySummary).not.toHaveBeenCalled()
    expect(mocks.getAdminSummary).not.toHaveBeenCalled()
  })

})
