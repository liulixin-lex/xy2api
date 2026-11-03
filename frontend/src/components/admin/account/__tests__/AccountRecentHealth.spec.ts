import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountRecentHealth from '../AccountRecentHealth.vue'

const { tt } = vi.hoisted(() => ({
  tt: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: tt }) }
})

const minutes = (n: number) => tt('admin.accounts.recentSpanMinutes', { n })

const failure = (over: Record<string, number> = {}) => ({
  calls: 1160,
  failures: 28,
  wilson_low: 0.0167,
  wilson_high: 0.0347,
  first_output_timeouts: 10,
  stream_failures: 8,
  other_failures: 10,
  span_seconds: 540,
  ...over
})
const firstOutput = (over: Record<string, number> = {}) => ({
  samples: 50,
  p50_ms: 6300,
  p90_ms: 19_200,
  slow_share: 0.24,
  span_seconds: 900,
  ...over
})
const cache = (over: Record<string, number> = {}) => ({
  samples: 50,
  kept: 47,
  zero: 1,
  partial: 2,
  mid: 3,
  full: 44,
  read_tokens: 4_100_000,
  prompt_tokens: 4_600_000,
  span_seconds: 1200,
  ...over
})

const mountHealth = (health: Record<string, unknown> | null, platform = 'openai', loadState?: string) =>
  mount(AccountRecentHealth, {
    props: { accountId: 42, platform, health: health as never, loadState: loadState as never }
  })
const line = (health: Record<string, unknown>, test: string, platform?: string) =>
  mountHealth({ failure: null, first_output: null, cache: null, ...health }, platform).get(`[data-test="${test}"]`)

describe('AccountRecentHealth', () => {
  it('stacks failures, first output and cache, with failures as a new-tab link to the account error requests', () => {
    const wrapper = mountHealth({ failure: failure(), first_output: firstOutput(), cache: cache() })
    const lines = wrapper.findAll('[data-test]').map(node => node.attributes('data-test'))
    const link = wrapper.get('[data-test="recent-failures"]')

    expect(lines).toEqual(['recent-failures', 'recent-first-output', 'recent-cache'])
    expect(link.text()).toBe('admin.accounts.recentFailureRate:{"failures":28,"calls":1160,"rate":"2.4%"}')
    expect(link.attributes('href')).toBe('/admin/usage?tab=errors&account_id=42')
    expect(link.attributes('target')).toBe('_blank')
    expect(link.attributes('rel')).toBe('noopener')
    expect(link.attributes('title')!.split('\n')).toEqual([
      tt('admin.accounts.recentFailureTitleInterval', { low: '1.6%', high: '3.5%', calls: 1160, span: minutes(9) }),
      tt('admin.accounts.recentFailureTitleKinds', { timeouts: 10, stream: 8, other: 10 }),
      'admin.accounts.recentFailureTitleRule',
      'admin.accounts.recentFailureTitleNote'
    ])
  })

  it('colours the failure rate by the Wilson lower bound once there are 20 calls', () => {
    const tone = (wilson_low: number) => line({ failure: failure({ wilson_low }) }, 'recent-failures').classes()
    expect(tone(0.0499)).toContain('text-gray-500')
    expect(tone(0.05)).toContain('text-amber-600')
    expect(tone(0.0999)).toContain('text-amber-600')
    expect(tone(0.1)).toContain('text-red-600')
  })

  it('truncates the shown lower bound so a grey line never reads 5.0%', () => {
    const grey = line({ failure: failure({ calls: 200, failures: 16, wilson_low: 0.04984, wilson_high: 0.12601 }) }, 'recent-failures')
    expect(grey.classes()).toContain('text-gray-500')
    expect(grey.attributes('title')!.split('\n')[0]).toBe(
      tt('admin.accounts.recentFailureTitleInterval', { low: '4.9%', high: '12.7%', calls: 200, span: minutes(9) })
    )
  })

  it('shows a grey failure count below 20 calls, whatever the bound, and the rate from exactly 20', () => {
    const count = line({ failure: failure({ calls: 19, failures: 6, wilson_low: 0.15 }) }, 'recent-failures')
    expect(count.text()).toBe('admin.accounts.recentFailureCount:{"failures":6,"calls":19}')
    expect(count.classes()).toContain('text-gray-500')

    const rate = line({ failure: failure({ calls: 20, failures: 6, wilson_low: 0.15 }) }, 'recent-failures')
    expect(rate.text()).toBe('admin.accounts.recentFailureRate:{"failures":6,"calls":20,"rate":"30.0%"}')
    expect(rate.classes()).toContain('text-red-600')
  })

  it('hides the failure line when nothing failed', () => {
    const wrapper = mountHealth({ failure: failure({ failures: 0 }), first_output: firstOutput(), cache: null })
    expect(wrapper.find('[data-test="recent-failures"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="recent-first-output"]').exists()).toBe(true)
  })

  it('shows the first output P90 with P50, slow share and span on hover, coloured by latency band from 50 samples', () => {
    const p90 = line({ first_output: firstOutput() }, 'recent-first-output')
    expect(p90.text()).toBe('admin.accounts.recentFirstOutputP90:{"value":"19.2s"}')
    expect(p90.classes()).toContain('text-amber-600')
    expect(p90.attributes('title')!.split('\n')).toEqual([
      'admin.accounts.recentFirstOutputTitleStats:{"p50":"6.3s","slow":"24%"}',
      tt('admin.accounts.recentFirstOutputTitleWindow', { count: 50, span: minutes(15) }),
      'admin.accounts.recentFirstOutputTitleRule'
    ])

    const tone = (p90_ms: number) => line({ first_output: firstOutput({ p90_ms }) }, 'recent-first-output').classes()
    expect(tone(9_999)).toContain('text-emerald-600')
    expect(tone(30_000)).toContain('text-orange-600')
    expect(tone(60_000)).toContain('text-red-600')
  })

  it('truncates the shown P90 so a green line never reads 10.0s', () => {
    const green = line({ first_output: firstOutput({ p90_ms: 9_999 }) }, 'recent-first-output')
    expect(green.text()).toBe('admin.accounts.recentFirstOutputP90:{"value":"9.9s"}')
    expect(green.classes()).toContain('text-emerald-600')
  })

  it('shows the first output P90 in grey from 20 to 49 samples, and a grey sample count below 20', () => {
    const grey = line({ first_output: firstOutput({ samples: 49, p90_ms: 61_000 }) }, 'recent-first-output')
    expect(grey.text()).toBe('admin.accounts.recentFirstOutputP90:{"value":"61.0s"}')
    expect(grey.classes()).toContain('text-gray-500')

    const twenty = line({ first_output: firstOutput({ samples: 20 }) }, 'recent-first-output')
    expect(twenty.text()).toBe('admin.accounts.recentFirstOutputP90:{"value":"19.2s"}')
    expect(twenty.classes()).toContain('text-gray-500')

    const few = line({ first_output: firstOutput({ samples: 19, p90_ms: 61_000 }) }, 'recent-first-output')
    expect(few.text()).toBe('admin.accounts.recentFirstOutputFew:{"count":19}')
    expect(few.classes()).toContain('text-gray-500')
    expect(few.attributes('title')).toBe(tt('admin.accounts.recentFirstOutputFewTitle', { count: 19, span: minutes(15), min: 20 }))
  })

  it('shows the cache kept rate with the four buckets and token read share on hover', () => {
    const kept = line({ cache: cache() }, 'recent-cache')
    expect(kept.text()).toBe('admin.accounts.recentCacheKept:{"value":"94%"}')
    expect(kept.classes()).toContain('text-gray-500')
    expect(kept.attributes('title')!.split('\n')).toEqual([
      'admin.accounts.recentCacheTitleBuckets:{"zero":"2%","partial":"4%","mid":"6%","full":"88%"}',
      'admin.accounts.recentCacheTitleTokens:{"value":"89%"}',
      tt('admin.accounts.recentCacheTitleWindow', { count: 50, span: minutes(20) }),
      'admin.accounts.recentCacheTitleRule'
    ])
  })

  it('colours the cache kept rate from 50 samples, amber below 90% and red below 75%', () => {
    const tone = (kept: number, samples = 50) => line({ cache: cache({ kept, samples }) }, 'recent-cache').classes()
    expect(tone(45)).toContain('text-gray-500')
    expect(tone(44)).toContain('text-amber-600')
    expect(tone(38)).toContain('text-amber-600')
    expect(tone(37)).toContain('text-red-600')
    expect(tone(10, 49)).toContain('text-gray-500')
  })

  it('never colours the cache line for Gemini, which caches implicitly', () => {
    const gemini = line({ cache: cache({ kept: 10 }) }, 'recent-cache', 'gemini')
    expect(gemini.classes()).toContain('text-gray-500')
    expect(gemini.attributes('title')!.split('\n').at(-1)).toBe('admin.accounts.recentCacheTitleGemini')
  })

  it('shows a grey cache sample count below 20 and the kept rate from exactly 20', () => {
    const few = line({ cache: cache({ samples: 19, kept: 1 }) }, 'recent-cache')
    expect(few.text()).toBe('admin.accounts.recentCacheFew:{"count":19}')
    expect(few.classes()).toContain('text-gray-500')

    const twenty = line({ cache: cache({ samples: 20, kept: 1 }) }, 'recent-cache')
    expect(twenty.text()).toBe('admin.accounts.recentCacheKept:{"value":"5%"}')
    expect(twenty.classes()).toContain('text-gray-500')
  })

  it('says under a minute instead of rounding a short span up', () => {
    const p90 = line({ first_output: firstOutput({ span_seconds: 59 }) }, 'recent-first-output')
    expect(p90.attributes('title')!.split('\n')[1]).toBe(
      tt('admin.accounts.recentFirstOutputTitleWindow', { count: 50, span: 'admin.accounts.recentSpanUnderMinute' })
    )
    const kept = line({ cache: cache({ span_seconds: 60 }) }, 'recent-cache')
    expect(kept.attributes('title')!.split('\n')[2]).toBe(tt('admin.accounts.recentCacheTitleWindow', { count: 50, span: minutes(1) }))
  })

  it('shows a dash explaining the empty windows once the batch has loaded without the account', () => {
    for (const health of [null, { failure: null, first_output: null, cache: null }]) {
      const dash = mountHealth(health, 'openai', 'loaded').get('[data-test="recent-empty"]')
      expect(dash.text()).toBe('-')
      expect(dash.attributes('title')).toBe('admin.accounts.recentHealthEmptyTitle')
    }
  })

  it('does not claim an empty window while the batch is loading or after it failed', () => {
    expect(mountHealth(null, 'openai', 'loading').get('[data-test="recent-empty"]').attributes('title')).toBe(
      'admin.accounts.recentHealthLoadingTitle'
    )
    expect(mountHealth(null, 'openai', 'failed').get('[data-test="recent-empty"]').attributes('title')).toBe(
      'admin.accounts.recentHealthFailedTitle'
    )
    // A row kept from an earlier load still shows its values while a refresh is in flight.
    expect(mountHealth({ failure: null, first_output: firstOutput(), cache: null }, 'openai', 'loading').find('[data-test="recent-first-output"]').exists()).toBe(true)
  })

  it('shows a dash saying nothing failed when there were only calls without failures or follow-ups', () => {
    const dash = mountHealth({ failure: failure({ failures: 0, calls: 30 }), first_output: null, cache: null }).get('[data-test="recent-empty"]')
    expect(dash.text()).toBe('-')
    expect(dash.attributes('title')).toBe('admin.accounts.recentHealthNoFailuresTitle:{"calls":30}')
  })
})
