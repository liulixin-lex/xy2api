import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
import zh from '@/i18n/locales/zh/admin/groupQualityGate'
import en from '@/i18n/locales/en/admin/groupQualityGate'
import type { GroupQualityAccountMetrics, GroupQualityGateAccountStatus, GroupQualityGatePolicies, GroupQualityGateStatus } from '@/types/groupQualityGate'
import {
  GROUP_QUALITY_BADGE_CLASSES,
  accountQualityMetricItems,
  buildAccountQualityCells,
  buildAccountQualityIndex,
  groupQualityGateView,
  groupQualityStateLabel,
  groupQualityTooltip,
  strictestQualityLimits,
  orderGroupsByQuality,
  qualityTooltipTop,
  type GroupQualityCell,
  type GroupQualitySources
} from '../groupQualityPresentation'

const messageCompiler = (message: unknown) => new Function('return ' + baseCompile(String(message), { mode: 'normal' }).code)()
const i18nFor = (locale: string) => createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } }).global
const clock = (iso: string) => `@${iso.slice(11, 16)}`

const account = (overrides: Partial<GroupQualityGateAccountStatus> = {}): GroupQualityGateAccountStatus => ({
  account_id: 101, state: 'eligible', until: null, reasons: [], strikes: 0, usable: true, passing: true,
  guard_held: false, pinned: false, vetoed: false, tier: 0, headroom: 0.4,
  metrics: { calls: 120, soft_failures: 2, error_rate: 0.017, warm_samples: 40, warm_ttft_p90_ms: 8000 }, ...overrides
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
const policy = (enabled: boolean) => ({ enabled, max_error_rate: 0.1, max_warm_ttft_p90_ms: 20000, fast_warm_ttft_p90_ms: 10000, pinned_account_ids: [] })
const policies = (groups: Record<string, boolean>): GroupQualityGatePolicies =>
  ({ version: 3, groups: Object.fromEntries(Object.entries(groups).map(([id, enabled]) => [id, policy(enabled)])) })
const sources = (overrides: Partial<GroupQualitySources> = {}): GroupQualitySources =>
  ({ policies: policies({ 7: true, 8: false }), policiesFailed: false, status: status([account()]), statusFailed: false, ...overrides })

describe('groupQualityGateView (policy × status)', () => {
  const openai = (id: number) => ({ id, platform: 'openai' })
  it.each([
    ['policy on, evaluated', openai(7), sources(), 'evaluated'],
    ['policy on, not in status yet', openai(9), sources({ policies: policies({ 9: true }) }), 'pending'],
    ['policy on, status still lists it as disabled', openai(8), sources({ policies: policies({ 8: true }) }), 'pending'],
    ['policy off, status still lists it as enabled', openai(7), sources({ policies: policies({ 7: false }) }), 'off'],
    ['no policy entry', openai(9), sources(), 'off'],
    ['policy on, status failed', openai(7), sources({ statusFailed: true }), 'failed'],
    ['policy off, status failed', openai(8), sources({ statusFailed: true }), 'off'],
    ['policies failed', openai(7), sources({ policiesFailed: true }), 'failed'],
    ['not loaded yet', openai(7), sources({ policies: null, status: null }), 'loading'],
    ['policy on, status not loaded yet', openai(7), sources({ status: null }), 'loading'],
    ['non-OpenAI group even when loading failed', { id: 7, platform: 'anthropic' }, sources({ policiesFailed: true }), 'off'],
    ['inactive group is never evaluated', { id: 9, platform: 'openai', status: 'inactive' }, sources({ policies: policies({ 9: true }) }), 'off']
  ] as const)('%s → %s', (_name, group, src, kind) => {
    expect(groupQualityGateView(group, src).kind).toBe(kind)
  })

  it('carries the evaluated summary', () => {
    const view = groupQualityGateView(openai(7), sources())
    expect(view.kind === 'evaluated' && view.group.summary).toEqual({ eligible: 1, ejected: 0, trial: 0 })
  })
})

describe('buildAccountQualityCells', () => {
  const groups = [{ id: 7, platform: 'openai' }, { id: 8, platform: 'openai' }, { id: 9, platform: 'openai' }, { id: 10, platform: 'gemini' }]
  const kinds = (src: GroupQualitySources, accountId = 101) =>
    Object.fromEntries([...buildAccountQualityCells(src)(accountId, groups)].map(([id, cell]) => [id, cell.kind]))

  it('uses the evaluated state, pending for new groups and new members, nothing for gates that are off', () => {
    const src = sources({ policies: policies({ 7: true, 8: false, 9: true }) })
    expect(kinds(src)).toEqual({ 7: 'eligible', 9: 'pending' })
    expect(kinds(src, 202)).toEqual({ 7: 'pending', 9: 'pending' })
  })

  it('marks gated groups as failed when loading failed and shows nothing while loading', () => {
    expect(kinds(sources({ statusFailed: true }))).toEqual({ 7: 'failed' })
    expect(kinds(sources({ policiesFailed: true }))).toEqual({ 7: 'failed', 8: 'failed', 9: 'failed' })
    expect(kinds(sources({ policies: null, status: null }))).toEqual({})
  })
})

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
    ['en', { state: 'ejected', guard_held: true }, 'Held by floor guard']
  ] as const)('%s %j → %s', (locale, row, label) => {
    expect(groupQualityStateLabel(qualityOf(account(row)), i18nFor(locale).t, clock)).toBe(label)
  })

  it('labels the neutral notices', () => {
    expect(groupQualityStateLabel({ kind: 'pending' }, i18nFor('zh').t, clock)).toBe('等待评估')
    expect(groupQualityStateLabel({ kind: 'failed' }, i18nFor('zh').t, clock)).toBe('读取失败')
    expect(groupQualityStateLabel({ kind: 'pending' }, i18nFor('en').t, clock)).toBe('Awaiting evaluation')
  })

  it('colours each kind with its own palette', () => {
    expect(GROUP_QUALITY_BADGE_CLASSES.eligible).toContain('emerald')
    expect(GROUP_QUALITY_BADGE_CLASSES.ejected).toContain('red')
    expect(GROUP_QUALITY_BADGE_CLASSES.trial).toContain('amber')
    expect(GROUP_QUALITY_BADGE_CLASSES.guard_held).toContain('sky')
    expect(GROUP_QUALITY_BADGE_CLASSES.pending).toContain('gray')
    expect(GROUP_QUALITY_BADGE_CLASSES.failed).toContain('gray')
  })
})

describe('orderGroupsByQuality', () => {
  it('puts ejected, guard-held, trial, failed, pending first and keeps the original order otherwise', () => {
    const groups = [{ id: 1 }, { id: 2 }, { id: 3 }, { id: 4 }, { id: 5 }, { id: 6 }, { id: 7 }]
    const quality = new Map([
      [2, qualityOf(account({ state: 'trial' }))],
      [3, qualityOf(account())],
      [4, qualityOf(account({ state: 'ejected' }))],
      [5, qualityOf(account({ state: 'ejected', guard_held: true }))],
      [6, { kind: 'pending' as const }],
      [7, { kind: 'failed' as const }]
    ])
    expect(orderGroupsByQuality(groups, quality).map(group => group.id)).toEqual([4, 5, 2, 7, 6, 3, 1])
    expect(orderGroupsByQuality(groups, undefined)).toBe(groups)
  })
})

describe('groupQualityTooltip', () => {
  const zh = i18nFor('zh').t
  const cells = (block: ReturnType<typeof groupQualityTooltip>) => block.rows.map(row => [row.label, row.value, row.note ?? '', row.limit, row.over])

  it('compresses an ejected group into a title, reason chips and a metric | measured | limit table', () => {
    const block = groupQualityTooltip('Codex', qualityOf(account({
      state: 'ejected', until: '2026-11-01T14:20:00Z', strikes: 2, reasons: ['error_rate', 'warm_ttft'], headroom: -0.18,
      metrics: { calls: 150, soft_failures: 21, error_rate: 0.14, warm_samples: 52, warm_ttft_p90_ms: 31800 }
    })), zh, clock)
    expect(block.title).toBe('Codex · 熔断至 @14:20（第 2 次）')
    expect(block.note).toBeUndefined()
    expect(block.reasons).toEqual(['错误率超线', '续轮首字超线'])
    expect(cells(block)).toEqual([['错误率', '14%', '', '10%', true], ['首字 p90', '31.8s', '', '20s', true]])
    expect(block.headroom).toEqual({ text: '余量 -18%', tone: 'danger' })
  })

  it('shows the full window for ejected groups, floor guard included, and member metrics for eligible and trial', () => {
    const postEjection = { calls: 4, soft_failures: 0, error_rate: null, warm_samples: 4, warm_ttft_p90_ms: null }
    const window = metrics({ calls: 60, soft_failures: 0, error_rate: 0, warm_samples: 103, warm_ttft_p90_ms: 29200 })
    const pinned = groupQualityTooltip('Codex', qualityOf(account({
      state: 'ejected', pinned: true, strikes: 1, reasons: ['warm_ttft'], headroom: null, metrics: postEjection
    })), zh, clock, window)
    expect(pinned.measuredNote).toBe('熔断时窗口')
    expect(cells(pinned)).toEqual([['错误率', '0%', '未达判定条件', '10%', false], ['首字 p90', '29.2s', '', '20s', true]])
    expect(pinned.headroom).toEqual({ text: '余量 -46%', tone: 'danger' })
    const held = groupQualityTooltip('Codex', qualityOf(account({ state: 'ejected', guard_held: true, strikes: 2, headroom: 0.5, metrics: postEjection })), zh, clock,
      metrics({ calls: 224, soft_failures: 75, error_rate: 0.335, warm_samples: 148, warm_ttft_p90_ms: 8200 }))
    expect([held.measuredNote, cells(held)[0][1], held.headroom?.text]).toEqual(['熔断时窗口', '33.5%', '余量 -235%'])
    const trial = groupQualityTooltip('Codex', qualityOf(account({ state: 'trial', headroom: null, metrics: postEjection })), zh, clock, window)
    expect([trial.measuredNote, cells(trial)[1][1], trial.headroom]).toEqual([undefined, '样本不足', undefined])
  })

  it('uses member metrics: below-threshold error ratio in grey, too few samples, no data; headroom only when close', () => {
    const below = groupQualityTooltip('Codex', qualityOf(account({
      headroom: 0.12, metrics: { calls: 12, soft_failures: 1, error_rate: null, warm_samples: 4, warm_ttft_p90_ms: null }
    })), zh, clock)
    expect(below.title).toBe('Codex · 合格')
    expect(cells(below)).toEqual([['错误率', '8.3%', '未达判定条件', '10%', false], ['首字 p90', '样本不足', '', '20s', false]])
    expect(below.headroom).toEqual({ text: '余量 12%', tone: 'warning' })
    const empty = groupQualityTooltip('Codex', qualityOf(account({
      headroom: null, metrics: { calls: 0, soft_failures: 0, error_rate: null, warm_samples: 0, warm_ttft_p90_ms: null }
    })), zh, clock)
    expect(cells(empty).map(row => row[1])).toEqual(['无数据', '无数据'])
    expect(empty.headroom).toBeUndefined()
    expect(groupQualityTooltip('Codex', qualityOf(account()), zh, clock).headroom).toBeUndefined()
  })

  it('keeps pinned and floor-guard notes to one short phrase, and flags stale data', () => {
    expect(groupQualityTooltip('Codex', qualityOf(account({ state: 'ejected', pinned: true, strikes: 1 })), zh, clock).note).toBe('固定账号，熔断时仍调度')
    const held = groupQualityTooltip('Codex', qualityOf(account({ state: 'ejected', guard_held: true, strikes: 1 }), { stale: true }), i18nFor('en').t, clock)
    expect([held.title, held.note, held.stale]).toEqual(['Codex · Held by floor guard', 'Kept to hold the minimum of usable accounts', 'Not evaluated for 5+ min, previous state kept'])
  })

  it('gives pending and failed notices a title and a next step only', () => {
    expect(groupQualityTooltip('Codex', { kind: 'pending' }, zh, clock)).toEqual({ title: 'Codex · 等待评估', note: '每分钟评估一次', reasons: [], rows: [] })
    expect(groupQualityTooltip('Codex', { kind: 'failed' }, zh, clock)).toEqual({ title: 'Codex · 读取失败', note: '刷新列表后重试', reasons: [], rows: [] })
  })
})

describe('accountQualityMetricItems', () => {
  const limits = { maxErrorRate: 0.1, maxWarmTtftMs: 20000 }
  const items = (m: GroupQualityAccountMetrics, l = limits) => accountQualityMetricItems(m, i18nFor('zh').t, l)

  it('writes the three full-window metrics compactly and lists only those with samples', () => {
    expect(items(metrics()).map(item => item.text)).toEqual(['错 2.2%', '首字 7.8s', '零缓存 3%'])
    expect(items(metrics())[0].title).toBe('错误率（近 15 分钟）：180 次调用，软失败 4 次')
    expect(items(metrics({ calls: 0, warm_samples: 0 })).map(item => item.key)).toEqual(['zero_cache'])
    expect(items(metrics({ calls: 0, warm_samples: 0, zero_cache_samples: 0 }))).toEqual([])
  })

  it('colours error rate and first token over the strictest group limit once samples are enough, and zero cache above 30%', () => {
    const alerts = (m: Partial<GroupQualityAccountMetrics>) => items(metrics(m)).filter(item => item.alert).map(item => item.key)
    expect(alerts({})).toEqual([])
    expect(alerts({ calls: 150, soft_failures: 21, error_rate: 0.14, warm_ttft_p90_ms: 31800, warm_zero_cache_rate: 0.31, zero_cache_samples: 20 })).toEqual(['error_rate', 'warm_ttft', 'zero_cache'])
    expect(alerts({ calls: 19, soft_failures: 5, error_rate: 0.26, warm_samples: 9, warm_ttft_p90_ms: 31800, zero_cache_samples: 19, warm_zero_cache_rate: 0.9 })).toEqual([])
    expect(accountQualityMetricItems(metrics({ calls: 150, soft_failures: 21, error_rate: 0.14 }), i18nFor('zh').t).some(item => item.alert)).toBe(false)
  })
})

describe('strictestQualityLimits', () => {
  it('takes the tightest limits among evaluated groups and ignores notices', () => {
    const cell = (maxErrorRate: number, maxWarmTtftMs: number) => ({ ...qualityOf(account()), maxErrorRate, maxWarmTtftMs })
    expect(strictestQualityLimits(new Map<number, GroupQualityCell>([[1, cell(0.1, 20000)], [2, cell(0.05, 30000)], [3, { kind: 'pending' }]])))
      .toEqual({ maxErrorRate: 0.05, maxWarmTtftMs: 20000 })
    expect(strictestQualityLimits(new Map<number, GroupQualityCell>([[3, { kind: 'pending' }]]))).toBeUndefined()
    expect(strictestQualityLimits(undefined)).toBeUndefined()
  })
})

describe('qualityTooltipTop', () => {
  const trigger = { top: 500, bottom: 540 }
  it('places the tooltip below, then above, then pinned inside the viewport by its real height', () => {
    expect(qualityTooltipTop(trigger, 200, 900)).toBe(546)
    expect(qualityTooltipTop(trigger, 420, 900)).toBe(74)
    expect(qualityTooltipTop(trigger, 600, 900)).toBe(292)
    expect(qualityTooltipTop(trigger, 1000, 900)).toBe(8)
  })
})
