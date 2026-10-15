import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountBalanceStateBadge from '../AccountBalanceStateBadge.vue'

const mocks = vi.hoisted(() => ({ recheck: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (d: string) => d }))
vi.mock('@/api/admin', () => ({ adminAPI: { resilience: { recheckBalance: mocks.recheck } } }))

describe('account balance state badge', () => {
  beforeEach(() => vi.clearAllMocks())

  it('stays hidden for a healthy or unknown balance', () => {
    expect(mount(AccountBalanceStateBadge, { props: { accountId: 1 } }).find('[data-testid="account-balance-state"]').exists()).toBe(false)
    expect(mount(AccountBalanceStateBadge, { props: { accountId: 1, record: { state: 'OK' } } }).find('[data-testid="account-balance-state"]').exists()).toBe(false)
  })

  it('rechecks an exhausted account and emits the new record', async () => {
    mocks.recheck.mockResolvedValue({ state: 'RECOVERING' })
    const w = mount(AccountBalanceStateBadge, { props: { accountId: 7, record: { state: 'EXHAUSTED', next_check_at: '0001-01-01T00:00:00Z' } } })
    expect(w.text()).toContain('admin.resilience.panel.states.EXHAUSTED')
    await w.get('[data-testid="account-balance-recheck"]').trigger('click'); await flushPromises()
    expect(mocks.recheck).toHaveBeenCalledWith(7)
    expect(w.emitted('updated')?.[0]).toEqual([{ state: 'RECOVERING' }])
    expect(w.emitted('success')).toHaveLength(1)
  })

  it('reports a failed recheck', async () => {
    mocks.recheck.mockRejectedValue({ status: 500, message: 'probe failed' })
    const w = mount(AccountBalanceStateBadge, { props: { accountId: 7, record: { state: 'SUSPECT' } } })
    await w.get('[data-testid="account-balance-recheck"]').trigger('click'); await flushPromises()
    expect(w.emitted('error')?.[0]).toEqual(['probe failed'])
  })
})
