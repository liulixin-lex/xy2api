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

const rule = (overrides: Record<string, unknown> = {}) => ({
  group_id: 3,
  group_name: 'claude',
  scheduler: 'share',
  effective: true,
  priority: 1,
  load_factor: 30,
  effective_load_factor: 30,
  healthy: true,
  expected_share: 0.25,
  share_if_healthy: 0.25,
  ...overrides
})

function mountRules(accountId: number) {
  return mount(ShareAccountRules, {
    props: { accountId },
    global: { stubs: { RouterLink: { props: ['to'], template: '<a :data-to="JSON.stringify(to)"><slot /></a>' } } }
  })
}

afterEach(() => vi.clearAllMocks())

describe('ShareAccountRules', () => {
  it('shows the expected share in every share group and links to it', async () => {
    accountRules.mockResolvedValue([rule(), rule({ group_id: 4, group_name: 'gpt', effective: false, healthy: false, expected_share: 0, share_if_healthy: 0.4 })])
    const wrapper = mountRules(12)
    await flushPromises()

    expect(accountRules).toHaveBeenCalledWith(12)
    const first = wrapper.get('[data-testid="share-account-rule-3"]')
    expect(first.text()).toContain('claude')
    expect(first.text()).toContain('"value":"25.0%"')
    expect(first.get('a').attributes('data-to')).toBe(JSON.stringify({ name: 'AdminShareScheduling', query: { group: '3' } }))
    const second = wrapper.get('[data-testid="share-account-rule-4"]')
    expect(second.text()).toContain('admin.shareScheduling.accountRules.notEffective')
    expect(second.text()).toContain('"value":"40.0%"')
  })

  it('renders nothing without share groups or when the rules cannot load', async () => {
    accountRules.mockResolvedValueOnce([])
    const empty = mountRules(12)
    await flushPromises()
    expect(empty.find('[data-testid="share-account-rules"]').exists()).toBe(false)

    accountRules.mockRejectedValueOnce(new Error('boom'))
    const failed = mountRules(13)
    await flushPromises()
    expect(failed.find('[data-testid="share-account-rules"]').exists()).toBe(false)
  })
})
