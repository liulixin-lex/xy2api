import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.inSelectedRange': 'in selected range',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheTotal': 'Cache',
  'usage.cacheBreakdown': 'Cache Token Breakdown',
  'usage.cacheCreationTokensLabel': 'Cache Creation',
  'usage.cacheReadTokensLabel': 'Cache Read',
  'usage.totalCost': 'Total Cost',
  'usage.accountCost': 'Cost',
  'usage.standardCost': 'Standard',
  'usage.avgDuration': 'Avg Duration',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${messages[key] ?? key}:${JSON.stringify(params)}` : messages[key] ?? key),
    }),
  }
})

const stats = {
  total_requests: 1,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 34,
  total_cache_creation_tokens: 12,
  total_cache_read_tokens: 22,
  total_tokens: 184,
  total_cost: 0.001,
  total_actual_cost: 0.001,
  total_account_cost: 0.001,
  average_duration_ms: 250,
}

describe('UsageStatsCards', () => {
  it('shows cache token breakdown values', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Cache: 34')
    expect(text).toContain('Cache Token Breakdown')
    expect(text).toContain('Cache Creation')
    expect(text).toContain('12')
    expect(text).toContain('Cache Read')
    expect(text).toContain('22')
  })

  it('keeps the cache tooltip out of the layout while it is hidden', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const tooltip = wrapper.findAll('span').find((el) => el.classes().includes('group-hover:block'))

    expect(tooltip).toBeDefined()
    // `opacity-0` hides the tooltip visually but keeps it in the layout, so its
    // fixed width still widens the document and causes horizontal scrolling on
    // narrow screens. `hidden` (display: none) takes it out of the flow.
    expect(tooltip?.classes()).toContain('hidden')
    expect(tooltip?.classes()).not.toContain('opacity-0')
  })
})

describe('UsageStatsCards user wait (admin)', () => {
  const mountCards = (value: Record<string, unknown>, e2eState?: 'loading' | 'failed' | 'loaded') => mount(UsageStatsCards, {
    props: { stats: { ...stats, ...value } as never, e2eState },
    global: { stubs: { Icon: true } },
  })
  const card = (wrapper: ReturnType<typeof mountCards>) => ({
    value: wrapper.get('[data-testid="usage-e2e-p50"]').text(),
    note: wrapper.get('[data-testid="usage-e2e-samples"]').text(),
  })

  it('shows the median end-to-end first output with the number of requests it is over', () => {
    expect(card(mountCards({ e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 1234 }, 'loaded'))).toEqual({
      value: '51.00s', note: `usage.e2eFirstOutputSamples:${JSON.stringify({ n: (1234).toLocaleString() })}`,
    })
    expect(card(mountCards({ e2e_first_output_p50_ms: 800, e2e_first_output_samples: 1 }, 'loaded')).note).toBe('usage.e2eFirstOutputSamples:{"n":"1"}')
  })

  it.each([
    ['still loading for the current filter', 'loading', { e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 }, { value: '…', note: 'usage.e2eFirstOutputLoading' }],
    ['failed for the current filter', 'failed', { e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 }, { value: 'usage.latencyUnknown', note: 'usage.e2eFirstOutputFailed' }],
    ['loaded without the figures', 'loaded', {}, { value: 'usage.latencyUnknown', note: 'usage.e2eFirstOutputMissing' }],
    ['loaded with no samples', 'loaded', { e2e_first_output_p50_ms: null, e2e_first_output_samples: 0 }, { value: 'usage.latencyUnknown', note: 'usage.e2eFirstOutputNone' }],
    ['not read while the list fails', 'blocked', { e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 }, { value: 'usage.latencyUnknown', note: 'usage.e2eFirstOutputBlocked' }],
    ['loaded with samples but no median', 'loaded', { e2e_first_output_samples: 12 }, { value: 'usage.latencyUnknown', note: 'usage.e2eFirstOutputP50Missing:{"n":"12"}' }],
  ] as const)('never shows another filter\'s median: %s', (_, state, value, expected) => {
    expect(card(mountCards(value, state))).toEqual(expected)
  })

  it('has no card on the user page, which passes no state', () => {
    expect(mountCards({ e2e_first_output_p50_ms: 51_000, e2e_first_output_samples: 12 }).find('[data-testid="usage-e2e-card"]').exists()).toBe(false)
  })
})
