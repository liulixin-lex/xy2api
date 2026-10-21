import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountStreamOnlyBadge from '../AccountStreamOnlyBadge.vue'

const mocks = vi.hoisted(() => ({ clear: vi.fn(), showSuccess: vi.fn(), showError: vi.fn() }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))
vi.mock('@/api/client', () => ({ apiClient: { delete: mocks.clear } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess, showError: mocks.showError }) }))

const NOW = Date.parse('2026-10-21T02:00:00Z')
const at = (ms: number) => new Date(NOW + ms).toISOString()
const H = 3_600_000

describe('account stream-only badge', () => {
  beforeEach(() => {
    vi.useFakeTimers({ now: NOW, toFake: ['Date'] })
    vi.clearAllMocks()
  })
  afterEach(() => { vi.useRealTimers() })

  it('shows the learned state with the hours left and the protocols in the title', () => {
    const w = mount(AccountStreamOnlyBadge, { props: { account: { id: 7, capability_learned: [
      { protocol: 'responses', mode: 'stream_only', expires_at: at(5 * H + 20 * 60_000) },
      { protocol: 'chat', mode: 'stream_only', expires_at: at(2 * H) }
    ] } } })
    const badge = w.get('[data-test="stream-only-badge"]')
    expect(badge.text()).toContain('admin.resilience.capability.badge')
    expect(badge.text()).toContain('admin.resilience.capability.hours:{\\"n\\":5}')
    expect(badge.attributes('title')).toContain('responses, chat')
  })

  it('counts minutes in the last hour and hides expired or unknown entries', () => {
    const soon = mount(AccountStreamOnlyBadge, { props: { account: { id: 7, capability_learned: [
      { protocol: 'responses', mode: 'stream_only', expires_at: at(25 * 60_000) }
    ] } } })
    expect(soon.text()).toContain('admin.resilience.capability.minutes:{\\"n\\":25}')

    for (const learned of [undefined, [], [{ protocol: 'responses', mode: 'stream_only', expires_at: at(-1000) }], [{ protocol: 'responses', mode: 'other', expires_at: at(H) }]]) {
      const w = mount(AccountStreamOnlyBadge, { props: { account: { id: 7, capability_learned: learned } } })
      expect(w.find('[data-test="stream-only-badge"]').exists()).toBe(false)
    }
  })

  it('clears the learned state and hands back the account without it', async () => {
    mocks.clear.mockResolvedValue(undefined)
    const account = { id: 7, name: 'a', capability_learned: [{ protocol: 'responses', mode: 'stream_only', expires_at: at(H) }] }
    const w = mount(AccountStreamOnlyBadge, { props: { account } })
    await w.get('[data-test="stream-only-clear"]').trigger('click'); await flushPromises()
    expect(mocks.clear).toHaveBeenCalledWith('/admin/accounts/7/capability-learning')
    expect(w.emitted('cleared')?.[0]).toEqual([{ id: 7, name: 'a', capability_learned: [] }])
    expect(mocks.showSuccess).toHaveBeenCalledWith('admin.resilience.capability.cleared')
  })

  it('keeps the state and reports the error when clearing fails', async () => {
    mocks.clear.mockRejectedValue({ message: 'boom' })
    const w = mount(AccountStreamOnlyBadge, { props: { account: { id: 7, capability_learned: [{ protocol: 'chat', mode: 'stream_only', expires_at: at(H) }] } } })
    await w.get('[data-test="stream-only-clear"]').trigger('click'); await flushPromises()
    expect(w.emitted('cleared')).toBeUndefined()
    expect(mocks.showError).toHaveBeenCalledWith('boom')
    expect(w.get('[data-test="stream-only-clear"]').attributes('disabled')).toBeUndefined()
  })
})
