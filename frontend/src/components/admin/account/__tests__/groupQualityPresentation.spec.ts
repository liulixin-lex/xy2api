import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
import zh from '@/i18n/locales/zh/admin/groupQualityGate'
import en from '@/i18n/locales/en/admin/groupQualityGate'
import type { GroupQualityAccountMetrics, GroupQualityGateAccountStatus, GroupQualityGateStatus } from '@/types/groupQualityGate'
import {
  GROUP_QUALITY_BADGE_CLASSES,
  accountQualityMetricItems,
  buildAccountQualityIndex,
  groupQualityStateLabel,
  groupQualityTooltip,
  orderGroupsByQuality
} from '../groupQualityPresentation'

const messageCompiler = (message: unknown) => new Function('return ' + baseCompile(String(message), { mode: 'normal' }).code)()
const i18nFor = (locale: string) => createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } }).global
const clock = (iso: string) => `@${iso.slice(11, 16)}`

const account = (overrides: Partial<GroupQualityGateAccountStatus> = {}): GroupQualityGateAccountStatus => ({
  account_id: 101, state: 'eligible', until: null, reasons: [], strikes: 0, usable: true, passing: true,
  guard_held: false, pinned: false, vetoed: false, tier: 0, headroom: 0.4, ...overrides
})
const metrics = (overrides: Partial<GroupQualityAccountMetrics> = {}): GroupQualityAccountMetrics => ({
  calls: 180, soft_failures: 4, error_rate: 0.022, warm_samples: 64, warm_ttft_p90_ms: 7800,
  zero_cache_samples: 40, warm_zero_cache_rate: 0.03, ...overrides
})
const status = (accounts: GroupQualityGateAccountStatus[], overrides: Partial<GroupQualityGateStatus> = {}): GroupQualityGateStatus => ({
  generated_at: '2026-11-01T00:00:00Z', stale: false, account_metrics: {},
  groups: [
    { group_id: 7, enabled: true, fast_warm_ttft_p90_ms: 10000, summary: { eligible: 1, ejected: 0, trial: 0 }, accounts },
    { group_id: 8, enabled: false, fast_warm_ttft_p90_ms: 10000, summary: { eligible: 0, ejected: 0, trial: 0 }, accounts }
  ],
  ...overrides
})
const qualityOf = (row: GroupQualityGateAccountStatus, extra: Partial<GroupQualityGateStatus> = {}) =>
  buildAccountQualityIndex(status([row], extra), null).get(row.account_id)!.get(7)!

describe('buildAccountQualityIndex', () => {
  it('indexes only enabled groups, keyed by account then group', () => {
    const index = buildAccountQualityIndex(status([account()]), null)
    expect([...index.keys()]).toEqual([101])
    expect([...index.get(101)!.keys()]).toEqual([7])
    expect(buildAccountQualityIndex(null, null).size).toBe(0)
  })

  it('maps guard-held accounts to their own kind and keeps the raw state for the rest', () => {
    expect(qualityOf(account({ state: 'ejected', guard_held: true })).kind).toBe('guard_held')
    expect(qualityOf(account({ state: 'ejected', pinned: true })).kind).toBe('ejected')
    expect(qualityOf(account({ state: 'trial' })).kind).toBe('trial')
    expect(qualityOf(account()).kind).toBe('eligible')
  })

  it('takes limits from the policy and falls back to the defaults for missing or zero fields', () => {
    const policies = { version: 3, groups: { 7: { enabled: true, max_error_rate: 0.2, max_warm_ttft_p90_ms: 0, fast_warm_ttft_p90_ms: 10000, pinned_account_ids: [] } } }
    const quality = buildAccountQualityIndex(status([account()]), policies).get(101)!.get(7)!
    expect(quality.maxErrorRate).toBe(0.2)
    expect(quality.maxWarmTtftMs).toBe(20000)
    expect(qualityOf(account())).toMatchObject({ maxErrorRate: 0.1, maxWarmTtftMs: 20000 })
  })
})

describe('state labels and colours', () => {
  it.each([
    ['zh', { state: 'eligible' }, '合格'],
    ['zh', { state: 'ejected', until: '2026-11-01T14:20:00Z' }, '熔断至 @14:20'],
    ['zh', { state: 'ejected' }, '熔断'],
    ['zh', { state: 'trial' }, '试用'],
    ['zh', { state: 'ejected', guard_held: true, until: '2026-11-01T14:20:00Z' }, '保底保留'],
    ['en', { state: 'ejected', until: '2026-11-01T14:20:00Z' }, 'Ejected until @14:20'],
    ['en', { state: 'ejected', guard_held: true }, 'Kept for floor']
  ] as const)('%s %j → %s', (locale, row, label) => {
    expect(groupQualityStateLabel(qualityOf(account(row)), i18nFor(locale).t, clock)).toBe(label)
  })

  it('colours each kind with its own palette', () => {
    expect(GROUP_QUALITY_BADGE_CLASSES.eligible).toContain('emerald')
    expect(GROUP_QUALITY_BADGE_CLASSES.ejected).toContain('red')
    expect(GROUP_QUALITY_BADGE_CLASSES.trial).toContain('amber')
    expect(GROUP_QUALITY_BADGE_CLASSES.guard_held).toContain('sky')
  })
})

describe('orderGroupsByQuality', () => {
  it('puts ejected, then guard-held, then trial first and keeps the original order otherwise', () => {
    const groups = [{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }, { id: 5 }]
    const quality = new Map([
      [2, qualityOf(account({ state: 'trial' }))],
      [3, qualityOf(account())],
      [4, qualityOf(account({ state: 'ejected' }))],
      [5, qualityOf(account({ state: 'ejected', guard_held: true }))]
    ])
    expect(orderGroupsByQuality(groups, quality).map(group => group.id)).toEqual([4, 5, 2, 3, 1])
    expect(orderGroupsByQuality(groups, undefined)).toBe(groups)
  })
})

describe('groupQualityTooltip', () => {
  it('lists state, pin, strikes, reasons, measured values with limits and headroom', () => {
    const quality = qualityOf(account({ state: 'ejected', until: '2026-11-01T14:20:00Z', pinned: true, strikes: 2, reasons: ['error_rate', 'warm_ttft'], headroom: -0.18 }))
    expect(groupQualityTooltip('Codex', quality, metrics({ error_rate: 0.118, warm_ttft_p90_ms: 21500 }), i18nFor('zh').t, clock)).toEqual([
      'Codex：熔断至 @14:20',
      '固定账号：熔断时仍参与本组调度',
      '连续熔断第 2 次',
      '原因：错误率超线、续轮首字超线',
      '错误率 11.8%，上限 10%',
      '续轮首字 p90 21.5s，上限 20s',
      '余量 -18%'
    ])
  })

  it('says when there is no data yet and when the evaluation is stale', () => {
    const quality = qualityOf(account({ state: 'ejected', guard_held: true, headroom: 0 }), { stale: true })
    expect(groupQualityTooltip('Codex', quality, metrics({ calls: 0, warm_samples: 0 }), i18nFor('en').t, clock)).toEqual([
      'Codex: Kept for floor',
      'Over the limits, but kept in scheduling so the group keeps its minimum of usable accounts',
      'Error rate: no data yet, limit 10%',
      'Warm first-token p90: no data yet, limit 20s',
      'Headroom 0%',
      'Evaluation data is more than 5 minutes old; the previous state is kept'
    ])
  })
})

describe('accountQualityMetricItems', () => {
  const texts = (m: GroupQualityAccountMetrics) => accountQualityMetricItems(m, i18nFor('zh').t)

  it('formats the three inline metrics', () => {
    expect(texts(metrics()).map(item => item.text)).toEqual(['错误率 2.2%', '续轮首字 p90 7.8s', '零缓存 3%'])
    expect(texts(metrics())[0].title).toBe('近 15 分钟 180 次调用，软失败 4 次')
  })

  it('shows a dash for metrics without samples', () => {
    expect(texts(metrics({ calls: 0, warm_samples: 0, zero_cache_samples: 0 })).map(item => item.text)).toEqual(['错误率 —', '续轮首字 p90 —', '零缓存 —'])
  })

  it('flags zero cache above 30% only once there are at least 20 samples', () => {
    const alert = (rate: number, samples: number) => texts(metrics({ warm_zero_cache_rate: rate, zero_cache_samples: samples }))[2].alert
    expect(alert(0.31, 20)).toBe(true)
    expect(alert(0.3, 40)).toBe(false)
    expect(alert(0.9, 19)).toBe(false)
    expect(texts(metrics()).filter(item => item.alert)).toEqual([])
  })
})
