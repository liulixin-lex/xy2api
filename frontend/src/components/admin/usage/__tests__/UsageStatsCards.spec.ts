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

// Browser check #1 and #7: the admin page's five cards fit at 1024 and 1440, and never show another filter's totals.
describe('UsageStatsCards admin layout and unread totals', () => {
  const big = {
    ...stats, total_requests: 12002, total_tokens: 652_440_000, total_input_tokens: 423_750_000, total_output_tokens: 1_230_000,
    total_actual_cost: 30750.12, total_cost: 30750.12, average_duration_ms: 1850, success: 11_990, rescued: 7, failed: 5
  }
  // The breakdown lines under the totals: tokens, request outcomes, cost; each carries recognizable figures.
  const breakdowns = (w: ReturnType<typeof mountCards>) => ({
    tokens: w.findAll('p').some((p) => p.text().includes('423.75M')),
    outcomes: w.find('[data-testid="usage-request-outcomes"]').exists(),
    cost: w.findAll('p').some((p) => p.text().includes('Standard'))
  })
  const mountCards = (e2eState?: 'loading' | 'failed' | 'loaded' | 'blocked') => mount(UsageStatsCards, {
    props: { stats: { ...big, e2e_first_output_p50_ms: 1850, e2e_first_output_samples: 1 } as never, e2eState },
    global: { stubs: { Icon: true } },
  })
  // The four totals by position: the shared markup carries no test ids, so the user page's DOM stays as it was.
  const totals = (w: ReturnType<typeof mountCards>) => w.findAll('.card').slice(0, 4).map((card) => card.get('p.text-xl').text())

  it('lays the admin cards out in three columns until 2xl, with text that wraps instead of being cut; the user page keeps four', () => {
    const admin = mountCards('loaded')
    expect(admin.get('div').classes()).toEqual(expect.arrayContaining(['grid', 'grid-cols-2', 'lg:grid-cols-3', '2xl:grid-cols-5']))
    expect(admin.get('div').classes()).not.toContain('lg:grid-cols-5')
    for (const card of admin.findAll('.card')) expect(card.get('div:nth-child(2)').classes()).toEqual(expect.arrayContaining(['min-w-0', '[overflow-wrap:anywhere]']))

    const user = mountCards()
    expect(user.get('div').classes()).toEqual(['grid', 'grid-cols-2', 'gap-4', 'lg:grid-cols-4'])
    expect(user.findAll('.card')).toHaveLength(4)
    expect(user.findAll('.card > div:nth-child(2)').map((el) => el.attributes('class') ?? '')).toEqual(['', '', 'min-w-0 flex-1', ''])
  })

  it.each([
    ['loading', ['…', '…', '…', '…']],
    ['failed', Array(4).fill('usage.latencyUnknown')],
    ['blocked', Array(4).fill('usage.latencyUnknown')]
  ] as const)('shows no totals on the admin page while its stats are %s', (state, expected) => {
    const w = mountCards(state)
    expect(totals(w)).toEqual(expected)
    expect(w.text()).not.toContain('12,002')
    expect(w.text()).not.toContain('30,750')
    // No breakdown of another filter's figures either (final-fe review, Astra item 9: the old check looked for a key
    // the copy stub never renders).
    expect(w.text()).not.toContain('423.75M')
    expect(breakdowns(w)).toEqual({ tokens: false, outcomes: false, cost: false })
  })

  // Totals carry two decimals with thousands separators (mobile list plan, typesetting).
  it('shows the totals and their breakdowns on the admin page once loaded, and always on the user page', () => {
    const loaded = mountCards('loaded')
    expect(totals(loaded)).toEqual(['12,002', '652.44M', '$30,750.12', '1.85s'])
    expect(breakdowns(loaded)).toEqual({ tokens: true, outcomes: true, cost: true })
    const user = mountCards()
    expect(totals(user)).toEqual(['12,002', '652.44M', '$30,750.12', '1.85s'])
    expect(breakdowns(user)).toEqual({ tokens: true, outcomes: true, cost: true })
  })
})
