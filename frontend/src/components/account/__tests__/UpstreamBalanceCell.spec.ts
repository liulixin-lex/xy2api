import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamBalanceCell from '../UpstreamBalanceCell.vue'
import type { UpstreamBalanceItem } from '@/api/admin/accounts'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${Object.values(params).join(',')}` : key
    })
  }
})

const NOW = Date.parse('2026-10-27T12:03:00Z')
const P = 'admin.accounts.upstreamBalance'

const iso = (ms: number) => new Date(ms).toISOString()

const makeItem = (overrides: Partial<UpstreamBalanceItem> = {}): UpstreamBalanceItem => ({
  account_id: 1,
  state: 'ok',
  amount_usd: 12.34,
  unlimited: false,
  basis: 'wallet',
  upstream_kind: 'sub2api',
  reason: null,
  observed_at: iso(NOW - 42_000),
  last_attempt_at: iso(NOW - 42_000),
  ...overrides
})

const mountCell = (item: UpstreamBalanceItem | undefined, now = NOW) =>
  mount(UpstreamBalanceCell, {
    props: { item, now },
    global: { stubs: { teleport: true } }
  })

const text = (wrapper: ReturnType<typeof mountCell>, testid: string) =>
  wrapper.get(`[data-testid="${testid}"]`).text()

describe('UpstreamBalanceCell', () => {
  it('shows the dollar amount and greys it out after three minutes without an update', async () => {
    // 后端最近一次尝试失败（last_attempt_at 晚于 observed_at）：超过 3 分钟没有成功即变灰。
    const failedAfter = (ageMs: number) =>
      makeItem({ observed_at: iso(NOW - ageMs), last_attempt_at: iso(NOW - 5_000), reason: 'upstream_error' })
    const wrapper = mountCell(failedAfter(179_000))
    const value = wrapper.get('[data-testid="upstream-balance-value"]')
    expect(value.text()).toBe('$12.34')
    expect(value.classes()).toContain('text-gray-800')

    await wrapper.setProps({ item: failedAfter(181_000) })
    expect(wrapper.get('[data-testid="upstream-balance-value"]').classes()).toContain('text-gray-400')
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('$12.34')
  })

  it('stays dark for up to ten minutes when no later attempt failed, and greys after that', async () => {
    const quiet = (ageMs: number) => makeItem({ observed_at: iso(NOW - ageMs), last_attempt_at: iso(NOW - ageMs) })
    const wrapper = mountCell(quiet(9 * 60_000))
    expect(wrapper.get('[data-testid="upstream-balance-value"]').classes()).toContain('text-gray-800')
    expect(text(wrapper, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.none`)

    await wrapper.setProps({ item: quiet(10 * 60_000 + 1_000) })
    expect(wrapper.get('[data-testid="upstream-balance-value"]').classes()).toContain('text-gray-400')
    expect(text(wrapper, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.stale`)
  })

  it('greys ∞ the same way as a number', () => {
    const wrapper = mountCell(makeItem({ unlimited: true, amount_usd: null, observed_at: iso(NOW - 11 * 60_000) }))
    const value = wrapper.get('[data-testid="upstream-balance-value"]')
    expect(value.text()).toBe('∞')
    expect(value.classes()).toContain('text-gray-400')
    expect(value.classes()).toContain('font-mono')
  })

  it('uses the clock-corrected now from the page', () => {
    // 服务端时间比本机快 5 分钟：observed_at = server_time - 60s，父组件传入的 now 已加上偏差，不应变灰。
    const serverNow = NOW + 5 * 60_000
    const wrapper = mountCell(
      makeItem({ observed_at: iso(serverNow - 60_000), last_attempt_at: iso(serverNow - 1_000), reason: 'rate_limited' }),
      serverNow
    )
    expect(wrapper.get('[data-testid="upstream-balance-value"]').classes()).toContain('text-gray-800')
    expect(text(wrapper, 'upstream-balance-updated')).toBe(`${P}.updatedMinutesAgo:1`)
  })

  it('shows ∞ for an unlimited key and — when there is no current value', () => {
    expect(mountCell(makeItem({ unlimited: true, amount_usd: null })).get('[data-testid="upstream-balance-value"]').text()).toBe('∞')

    const unavailable = mountCell(makeItem({ state: 'unavailable', amount_usd: 5, reason: 'auth_failed' }))
    expect(unavailable.get('[data-testid="upstream-balance-value"]').text()).toBe('—')
    expect(unavailable.get('[data-testid="upstream-balance-value"]').classes()).not.toContain('font-mono')

    const notApplicable = mountCell(
      makeItem({ state: 'not_applicable', amount_usd: null, basis: null, upstream_kind: null, observed_at: null, last_attempt_at: null })
    )
    expect(notApplicable.get('[data-testid="upstream-balance-value"]').text()).toBe('—')
    expect(text(notApplicable, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.notApplicable`)

    const okWithoutNumber = mountCell(makeItem({ amount_usd: null }))
    expect(okWithoutNumber.get('[data-testid="upstream-balance-value"]').text()).toBe('—')
  })

  it('always lists the basis, the reason and how long ago it was updated', () => {
    const wrapper = mountCell(makeItem())
    expect(wrapper.find('[data-testid="upstream-balance-details"]').exists()).toBe(true)
    expect(text(wrapper, 'upstream-balance-basis')).toBe(`${P}.basisLine:${P}.basis.wallet`)
    expect(text(wrapper, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.none`)
    expect(text(wrapper, 'upstream-balance-updated')).toBe(`${P}.updatedSecondsAgo:42`)

    const failing = mountCell(
      makeItem({ state: 'unavailable', amount_usd: null, basis: null, reason: 'rate_limited', observed_at: null })
    )
    expect(text(failing, 'upstream-balance-basis')).toBe(`${P}.basisLine:${P}.basis.unknown`)
    expect(text(failing, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.rateLimited`)
    expect(text(failing, 'upstream-balance-updated')).toBe(`${P}.neverUpdated`)

    const pending = mountCell(
      makeItem({ state: 'unavailable', amount_usd: null, basis: null, upstream_kind: null, observed_at: null, last_attempt_at: null })
    )
    expect(text(pending, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.pending`)

    // new-api 只用 Key 查询时口径通常未知（basis = null）：仍显示金额，口径写「未知」。
    const keyOnly = mountCell(makeItem({ basis: null, upstream_kind: 'newapi' }))
    expect(keyOnly.get('[data-testid="upstream-balance-value"]').text()).toBe('$12.34')
    expect(text(keyOnly, 'upstream-balance-basis')).toBe(`${P}.basisLine:${P}.basis.unknown`)

    expect(text(mountCell(makeItem({ basis: 'key' })), 'upstream-balance-basis')).toBe(`${P}.basisLine:${P}.basis.key`)
    expect(text(mountCell(makeItem({ basis: 'subscription' })), 'upstream-balance-basis')).toBe(
      `${P}.basisLine:${P}.basis.subscription`
    )
  })

  it('labels the update age in seconds, minutes, hours and days', () => {
    const updated = (ageMs: number) =>
      text(mountCell(makeItem({ observed_at: iso(NOW - ageMs), last_attempt_at: iso(NOW - ageMs) })), 'upstream-balance-updated')
    expect(updated(59_999)).toBe(`${P}.updatedSecondsAgo:59`)
    expect(updated(60_000)).toBe(`${P}.updatedMinutesAgo:1`)
    expect(updated(3_599_000)).toBe(`${P}.updatedMinutesAgo:59`)
    expect(updated(3_600_000)).toBe(`${P}.updatedHoursAgo:1`)
    expect(updated(86_399_000)).toBe(`${P}.updatedHoursAgo:23`)
    expect(updated(86_400_000)).toBe(`${P}.updatedDaysAgo:1`)
    // 未来时间（时钟仍有偏差）按 0 秒显示。
    expect(updated(-5_000)).toBe(`${P}.updatedSecondsAgo:0`)
  })

  it('maps every reason code to its own label', () => {
    const cases: Array<[string, string]> = [
      ['unidentified', 'unidentified'],
      ['rate_limited', 'rateLimited'],
      ['auth_failed', 'authFailed'],
      ['token_invalid', 'tokenInvalid'],
      ['key_ungrouped', 'keyUngrouped'],
      ['key_exhausted', 'keyExhausted'],
      ['upstream_error', 'upstreamError'],
      ['no_data', 'noData'],
      ['redis_unavailable', 'redisUnavailable'],
      ['something_new', 'unknown']
    ]
    for (const [code, key] of cases) {
      const wrapper = mountCell(makeItem({ reason: code as UpstreamBalanceItem['reason'] }))
      expect(text(wrapper, 'upstream-balance-reason')).toBe(`${P}.reasonLine:${P}.reason.${key}`)
    }
  })

  it('formats amounts with two decimals and thousands separators', () => {
    const value = (amount: number) =>
      mountCell(makeItem({ amount_usd: amount })).get('[data-testid="upstream-balance-value"]').text()
    expect(value(1234.5)).toBe('$1,234.50')
    expect(value(0)).toBe('$0.00')
    // 负数（上游欠费）原样显示；四舍五入到 0 的负数不显示 -$0.00。
    expect(value(-3.2)).toBe('-$3.20')
    expect(value(-0.004)).toBe('$0.00')
    expect(value(0.005)).toBe('$0.01')
  })

  it('shows a plain dash without a tooltip before the first answer', () => {
    const wrapper = mountCell(undefined)
    expect(wrapper.find('[data-testid="upstream-balance-details"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="upstream-balance-value"]').text()).toBe('—')
  })
})
