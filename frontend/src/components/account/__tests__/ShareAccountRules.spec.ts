import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { accountRules } = vi.hoisted(() => ({ accountRules: vi.fn() }))

vi.mock('@/api/admin/shareScheduling', () => ({ default: { accountRules } }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key} ${JSON.stringify(params)}` : key),
      te: () => false
    })
  }
})

import ShareAccountRules from '../ShareAccountRules.vue'

function mountRules(accountId: number) {
  return mount(ShareAccountRules, { props: { accountId }, global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } })
}

afterEach(() => vi.clearAllMocks())

describe('ShareAccountRules', () => {
  it('shows the resolved rule of every share scheduling group', async () => {
    accountRules.mockResolvedValue([
      { group_id: 3, group_name: 'claude', scheduler: 'share', effective: true, configured: true, tier: 1, weight: 5, drain: true, cache_domain: 'ws-a' },
      { group_id: 4, group_name: '', scheduler: 'shadow', effective: false, configured: false, tier: 2, weight: 10, drain: false, cache_domain: 'account:7' }
    ])
    const wrapper = mountRules(7)
    await flushPromises()
    expect(accountRules).toHaveBeenCalledWith(7)
    const configured = wrapper.get('[data-testid="share-account-rule-3"]').text()
    expect(configured).toContain('claude')
    expect(configured).toContain('{"tier":1}')
    expect(configured).toContain('{"weight":5}')
    expect(configured).toContain('{"domain":"ws-a"}')
    expect(configured).toContain('accountRules.drain')
    const defaults = wrapper.get('[data-testid="share-account-rule-4"]').text()
    expect(defaults).toContain('#4')
    expect(defaults).toContain('accountRules.ownDomain')
    expect(defaults).toContain('accountRules.notEffective')
    expect(defaults).toContain('accountRules.defaults')
  })

  it('renders nothing without rules or when loading fails', async () => {
    accountRules.mockResolvedValueOnce([])
    const empty = mountRules(7)
    await flushPromises()
    expect(empty.find('[data-testid="share-account-rules"]').exists()).toBe(false)

    accountRules.mockRejectedValueOnce(new Error('unavailable'))
    const failed = mountRules(8)
    await flushPromises()
    expect(failed.find('[data-testid="share-account-rules"]').exists()).toBe(false)
  })
})
