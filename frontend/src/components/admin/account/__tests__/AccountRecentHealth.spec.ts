import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountRecentHealth from '../AccountRecentHealth.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
    })
  }
})

const mountHealth = (health: Record<string, unknown> | null) =>
  mount(AccountRecentHealth, {
    props: { accountId: 42, health: health as never },
    slots: { default: '<span data-test="status">正常</span>' }
  })

describe('AccountRecentHealth', () => {
  it('shows the recent error count beside the status as a new-tab link to the account error requests', () => {
    const wrapper = mountHealth({ first_token_avg_ms: null, first_token_samples: 0, recent_errors: 7 })
    const link = wrapper.get('[data-test="recent-errors"]')

    expect(wrapper.get('[data-test="status"]').text()).toBe('正常')
    expect(link.text()).toBe('7')
    expect(link.attributes('href')).toBe('/admin/usage?tab=errors&account_id=42')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener')
    expect(link.classes()).toContain('text-red-600')
    expect(link.attributes('title')).toBe('admin.accounts.recentErrorsTitle:{"count":7}')
  })

  it('shows the 3-minute first token average under the status, coloured by latency band', () => {
    const fast = mountHealth({ first_token_avg_ms: 850, first_token_samples: 3, recent_errors: 0 })
    const average = fast.get('[data-test="first-token-avg"]')
    expect(average.text()).toBe('admin.accounts.firstTokenAvg:{"value":"850ms"}')
    expect(average.classes()).toContain('text-emerald-600')
    expect(average.attributes('title')).toBe('admin.accounts.firstTokenAvgTitle:{"count":3}')

    const slow = mountHealth({ first_token_avg_ms: 31_250, first_token_samples: 1, recent_errors: 0 })
    expect(slow.get('[data-test="first-token-avg"]').text()).toBe('admin.accounts.firstTokenAvg:{"value":"31.25s"}')
    expect(slow.get('[data-test="first-token-avg"]').classes()).toContain('text-orange-600')
  })

  it('shows only the status when there were no requests or errors', () => {
    for (const health of [null, { first_token_avg_ms: null, first_token_samples: 0, recent_errors: 0 }]) {
      const wrapper = mountHealth(health)
      expect(wrapper.get('[data-test="status"]').exists()).toBe(true)
      expect(wrapper.find('[data-test="recent-errors"]').exists()).toBe(false)
      expect(wrapper.find('[data-test="first-token-avg"]').exists()).toBe(false)
    }
  })
})
