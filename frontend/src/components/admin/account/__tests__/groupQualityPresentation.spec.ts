import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
import zh from '@/i18n/locales/zh/admin/groupQualityGate'
import en from '@/i18n/locales/en/admin/groupQualityGate'
import type {
  GroupQualityGateAccountStatus,
  GroupQualityGateGroupStatus,
  GroupQualityGatePolicies,
  GroupQualityGateStatus
} from '@/types/groupQualityGate'
import {
  GROUP_QUALITY_BADGE_CLASSES,
  accountQualityAlertItems,
  activeLatencyHold,
  buildAccountQualityCells,
  buildAccountQualityIndex,
  groupQualityBlock,
  groupQualityGateView,
  groupQualityPanel,
  groupQualityStateLabel,
  groupQualitySummaryTitle,
  orderGroupsByQuality,
  qualityDimensionRows,
  qualityTooltipTop,
  type AccountGroupQuality,
  type GroupQualityCell,
  type GroupQualitySources
} from '../groupQualityPresentation'

const messageCompiler = (message: unknown) => new Function('return ' + baseCompile(String(message), { mode: 'normal' }).code)()
const i18nFor = (locale: string) => createI18n({ messageCompiler, legacy: false, locale, messages: { zh: { admin: zh }, en: { admin: en } } }).global
const zhT = i18nFor('zh').t
const clock = (iso: string) => `@${iso.slice(11, 16)}`
const NOW = Date.parse('2026-11-04T10:00:00Z')

// design §5.3 的示例形状。
const account = (overrides: Partial<GroupQualityGateAccountStatus> = {}): GroupQualityGateAccountStatus => ({
  account_id: 101,
  status: 'passing',
  tier: 0,
  pinned: false,
  latency_hold: null,
  ttft: { state: 'pass', value: 9800, factor: 1.08, samples: 23, sessions: 6, bad_until: null },
  error: { state: 'pass', value: 0.04, samples: 80, failures: 3, wilson_low: 0.013, wilson_high: 0.105, bad_until: null },
  cache: { state: 'pass', value: 0.82, samples: 24, wilson_low: 0.62, wilson_high: 0.93, bad_until: null },
  ...overrides
})
const group = (accounts: GroupQualityGateAccountStatus[], overrides: Partial<GroupQualityGateGroupStatus> = {}): GroupQualityGateGroupStatus => ({
  group_id: 18,
  enabled: true,
  policy: { enabled: true, max_ttft_p90_seconds: 15, max_error_rate: 0.1, min_cache_keep_rate: 0.5, strict: false },
  expected_p90_ms: 9100,
  expected_samples: 87,
  summary: { passing: 3, unknown: 2, retest: 1, failing: 1, pinned: 0 },
  accounts,
  ...overrides
})
const status = (groups: GroupQualityGateGroupStatus[], overrides: Partial<GroupQualityGateStatus> = {}): GroupQualityGateStatus => ({
  generated_at: '2026-11-04T10:00:00Z', stale: false, groups, ...overrides
})
const policies = (ids: number[], enabled = true): GroupQualityGatePolicies => ({
  version: 3,
  groups: Object.fromEntries(ids.map(id => [String(id), {
    enabled: !enabled,
    v3: { enabled, max_ttft_p90_seconds: 15, max_error_rate: 0.1, min_cache_keep_rate: 0.5, strict: false }
  }]))
})
const sources = (overrides: Partial<GroupQualitySources> = {}): GroupQualitySources => ({
  policies: policies([18]), policiesFailed: false, status: status([group([account()])]), statusFailed: false, ...overrides
})
const cellOf = (acc: GroupQualityGateAccountStatus, grp: Partial<GroupQualityGateGroupStatus> = {}, stale = false): AccountGroupQuality => {
  const g = group([acc], grp)
  return { kind: acc.status, account: acc, group: g, stale }
}

describe('groupQualityGateView', () => {
  const openai = { id: 18, platform: 'openai' }

  it('follows the v3 switch, not the v2 one', () => {
    expect(groupQualityGateView(openai, sources()).kind).toBe('evaluated')
    // v2 开、v3 关：按关闭显示。
    expect(groupQualityGateView(openai, sources({ policies: policies([18], false) })).kind).toBe('off')
    expect(groupQualityGateView(openai, sources({ policies: { version: 1, groups: {} } })).kind).toBe('off')
  })

  it('is off for other platforms and inactive groups, failed or loading while reads are not done', () => {
    expect(groupQualityGateView({ id: 18, platform: 'anthropic' }, sources()).kind).toBe('off')
    expect(groupQualityGateView({ ...openai, status: 'inactive' }, sources()).kind).toBe('off')
    expect(groupQualityGateView(openai, sources({ policiesFailed: true })).kind).toBe('failed')
    expect(groupQualityGateView(openai, sources({ statusFailed: true })).kind).toBe('failed')
    expect(groupQualityGateView(openai, sources({ policies: null })).kind).toBe('loading')
    expect(groupQualityGateView(openai, sources({ status: status([]) })).kind).toBe('pending')
  })
})

describe('buildAccountQualityIndex / buildAccountQualityCells', () => {
  it('keys each account by group with its own status, group standard and composition', () => {
    const index = buildAccountQualityIndex(status([group([account({ status: 'unknown' })]), group([account()], { group_id: 47, enabled: false })], { stale: true }))
    const cell = index.get(101)!.get(18)!
    expect(cell.kind).toBe('unknown')
    expect(cell.group.expected_p90_ms).toBe(9100)
    expect(cell.stale).toBe(true)
    expect(index.get(101)!.has(47)).toBe(false)
  })

  it('marks a new member of an evaluated group as pending and leaves gate-off groups out', () => {
    const cellsFor = buildAccountQualityCells(sources())
    const cells = cellsFor(202, [{ id: 18, platform: 'openai' }, { id: 25, platform: 'openai' }])
    expect(cells.get(18)).toEqual({ kind: 'pending' })
    expect(cells.has(25)).toBe(false)
  })
})

describe('state labels and colours', () => {
  it.each([
    ['passing', '达标', 'Passing'],
    ['unknown', '未知', 'Unknown'],
    ['retest', '待复测', 'Retest'],
    ['failing', '不达标', 'Failing'],
    ['pinned', '固定', 'Pinned'],
    ['pending', '等待评估', 'Awaiting evaluation'],
    ['failed', '读取失败', 'Load failed']
  ] as const)('labels %s', (kind, zhLabel, enLabel) => {
    const cell = { kind } as GroupQualityCell
    expect(groupQualityStateLabel(cell, zhT)).toBe(zhLabel)
    expect(groupQualityStateLabel(cell, i18nFor('en').t)).toBe(enLabel)
  })

  it('never shows an unknown account as passing', () => {
    const unknown = cellOf(account({ status: 'unknown', cache: { ...account().cache, state: 'none' } }))
    expect(groupQualityStateLabel(unknown, zhT)).toBe('未知')
    expect(GROUP_QUALITY_BADGE_CLASSES.unknown).not.toBe(GROUP_QUALITY_BADGE_CLASSES.passing)
    expect(GROUP_QUALITY_BADGE_CLASSES.unknown).not.toContain('emerald')
    expect(groupQualityBlock('主力', unknown, zhT, clock).title).toBe('主力 · 未知')
    // 只有确实三维达标的才在面板里写“达标”。
    expect(groupQualityBlock('主力', unknown, zhT, clock).title).not.toContain('达标')
  })

  it('shows a pinned account as pinned with its actual dimension states', () => {
    const pinned = cellOf(account({ status: 'pinned', pinned: true, error: { ...account().error, state: 'fail', value: 0.2 } }))
    const block = groupQualityBlock('企业', pinned, zhT, clock)
    expect(block.title).toBe('企业 · 固定')
    expect(block.note).toContain('质量档固定为 0 档')
    expect(block.rows.find(row => row.key === 'error')).toMatchObject({ state: 'fail', stateLabel: '不达标', value: '20%' })
  })
})

describe('qualityDimensionRows', () => {
  it('shows state, value, samples and the group standard for each dimension', () => {
    const rows = qualityDimensionRows(cellOf(account({ cache: { ...account().cache, state: 'none', samples: 14 } })), zhT)
    expect(rows.map(row => [row.key, row.stateLabel, row.value, row.detail, row.samples, row.limit])).toEqual([
      ['ttft', '达标', '9.8s', '系数 ×1.08', '23 · 6 会话', '≤ 15s'],
      ['error', '达标', '4%', '区间 1.3–10.5%', '3/80', '≤ 10%'],
      ['cache', '无结论', '82%', '区间 62–93%', '14', '≥ 50%']
    ])
  })

  it('says the group has too few samples when e_G is unavailable, keeping the account factor', () => {
    const quality = cellOf(account({ ttft: { state: 'none', value: null, factor: 1.3, samples: 15, sessions: 4, bad_until: null } }), { expected_p90_ms: null, expected_samples: 12 })
    const ttft = qualityDimensionRows(quality, zhT)[0]
    expect(ttft).toMatchObject({ state: 'none', stateLabel: '无结论', value: '本组样本不足', detail: '系数 ×1.30' })
    const block = groupQualityBlock('低价', quality, zhT, clock)
    expect(block.composition).toEqual({ text: '首字：本组样本不足（12/50 条）', insufficient: true })
    expect(qualityDimensionRows(quality, i18nFor('en').t)[0].value).toBe('Group too thin')
  })

  it('uses a dash for a dimension without statistics', () => {
    const rows = qualityDimensionRows(cellOf(account({ error: { state: 'none', value: null, samples: 0, failures: 0, wilson_low: null, wilson_high: null, bad_until: null } })), zhT)
    expect(rows[1]).toMatchObject({ value: '—', detail: undefined, samples: '0/0' })
  })
})

describe('groupQualityBlock', () => {
  it('lists the bad memory of each dimension that still has one', () => {
    const quality = cellOf(account({
      status: 'retest', tier: 1,
      ttft: { ...account().ttft, state: 'none', bad_until: '2026-11-04T11:20:00Z' },
      cache: { ...account().cache, bad_until: '2026-11-04T11:42:00Z' }
    }), { policy: { enabled: true, max_ttft_p90_seconds: 10, max_error_rate: 0.08, min_cache_keep_rate: 0.5, strict: true } })
    const block = groupQualityBlock('企业', quality, zhT, clock)
    expect(block.title).toBe('企业 · 待复测')
    expect(block.strict).toBe(true)
    expect(block.badMemory).toBe('坏记忆：首字至 @11:20、缓存至 @11:42')
    expect(block.composition).toEqual({ text: '本组构成 P90 9.1s（87 条）', insufficient: false })
  })

  it('explains the pending and failed notices', () => {
    expect(groupQualityBlock('主力', { kind: 'pending' }, zhT, clock)).toMatchObject({ title: '主力 · 等待评估', note: '每分钟评估一次', rows: [] })
    expect(groupQualityBlock('主力', { kind: 'failed' }, zhT, clock)).toMatchObject({ title: '主力 · 读取失败', note: '刷新列表后重试' })
  })
})

describe('latency hold', () => {
  const hold = (until: string, models: string[]) => ({ until, models })

  it('merges models and takes the latest end across groups', () => {
    const cells = [
      cellOf(account({ latency_hold: hold('2026-11-04T10:06:00Z', ['gpt-5.5']) })),
      cellOf(account({ latency_hold: hold('2026-11-04T10:12:00Z', ['gpt-5.5', 'gpt-5.4']) }))
    ]
    expect(activeLatencyHold(cells, NOW)).toEqual({ until: '2026-11-04T10:12:00.000Z', models: ['gpt-5.5', 'gpt-5.4'] })
  })

  it('drops an expired hold without ever saying recovered', () => {
    const expired = new Map<number, GroupQualityCell>([[18, cellOf(account({ latency_hold: hold('2026-11-04T09:59:00Z', ['gpt-5.5']) }))]])
    expect(activeLatencyHold(expired.values(), NOW)).toBeNull()
    const panel = groupQualityPanel([{ id: 18, name: '主力' }], expired, zhT, clock, NOW)!
    expect(panel.latencyHold).toBeUndefined()
    expect(panel.indicator).toBe('none')
    expect(JSON.stringify(panel)).not.toMatch(/恢复|recovered/i)
    expect(accountQualityAlertItems(expired, zhT, clock, NOW)).toEqual([])
  })

  it('states that the end of a hold only lifts the ordering', () => {
    const quality = new Map<number, GroupQualityCell>([[18, cellOf(account({ latency_hold: hold('2026-11-04T10:12:00Z', ['gpt-5.5']) }))]])
    const panel = groupQualityPanel([{ id: 18, name: '主力' }], quality, zhT, clock, NOW)!
    expect(panel.indicator).toBe('hold')
    expect(panel.latencyHold).toEqual({ text: '判慢让位：gpt-5.5，后排至 @10:12', note: '到期只解除后排，不改变门槛状态' })
    expect(JSON.stringify(panel)).not.toContain('已恢复')
  })
})

describe('groupQualityPanel', () => {
  it('orders blocks by severity, counts attention and flags stale data once', () => {
    const groups = [{ id: 1, name: 'A' }, { id: 2, name: 'B' }, { id: 3, name: 'C' }, { id: 4, name: 'D' }]
    const quality = new Map<number, GroupQualityCell>([
      [1, cellOf(account())],
      [2, cellOf(account({ status: 'unknown' }), {}, true)],
      [3, cellOf(account({ status: 'failing', tier: 2 }))],
      [4, cellOf(account({ status: 'retest', tier: 1 }))]
    ])
    const panel = groupQualityPanel(orderGroupsByQuality(groups, quality), quality, zhT, clock, NOW)!
    expect(panel.blocks.map(block => block.title)).toEqual(['C · 不达标', 'D · 待复测', 'B · 未知', 'A · 达标'])
    expect(panel.indicator).toBe('failing')
    expect(panel.attentionCount).toBe(2)
    expect(panel.stale).toContain('数据陈旧')
  })

  it('returns null when no group of the account has the gate on', () => {
    expect(groupQualityPanel([{ id: 1, name: 'A' }], new Map(), zhT, clock, NOW)).toBeNull()
  })
})

describe('accountQualityAlertItems', () => {
  it('lists only failing dimensions, taking the worst first token across groups, and an active hold', () => {
    const quality = new Map<number, GroupQualityCell>([
      [1, cellOf(account({ status: 'failing', ttft: { ...account().ttft, state: 'fail', value: 18200 } }))],
      [2, cellOf(account({ status: 'failing', ttft: { ...account().ttft, state: 'fail', value: 21000 }, latency_hold: { until: '2026-11-04T10:12:00Z', models: ['gpt-5.5'] } }))],
      [3, { kind: 'pending' }]
    ])
    const items = accountQualityAlertItems(quality, zhT, clock, NOW)
    expect(items.map(item => [item.key, item.text, item.tone])).toEqual([
      ['ttft', '首字 21s', 'danger'],
      ['latency_hold', '判慢至 @10:12', 'warning']
    ])
    expect(items[0].title).toBe('首字在 2 个门槛分组不达标')
  })

  it('is empty for passing and unknown accounts', () => {
    const quality = new Map<number, GroupQualityCell>([[1, cellOf(account())], [2, cellOf(account({ status: 'unknown', cache: { ...account().cache, state: 'none' } }))]])
    expect(accountQualityAlertItems(quality, zhT, clock, NOW)).toEqual([])
  })
})

describe('groupQualitySummaryTitle', () => {
  it('lists the five counts and the group composition', () => {
    expect(groupQualitySummaryTitle(group([]), zhT)).toBe('达标 3 · 未知 2 · 待复测 1 · 不达标 1 · 固定 0；本组构成 P90 9.1s（87 条）')
    expect(groupQualitySummaryTitle(group([], { expected_p90_ms: null, expected_samples: 30 }), zhT)).toContain('本组样本不足（30/50 条）')
  })
})

describe('gate wording (red team B8)', () => {
  it('describes unknown, migration targets, restarts and stale data accurately', () => {
    const zhT2 = i18nFor('zh').t
    expect(zhT2('admin.groupQualityGate.unknownBadgeTitle', { group: '主力' })).toBe('主力 · 未知：暂无结论（样本不足或处于中间带），暂按 0 档参与排序')
    expect(zhT2('admin.groupQualityGate.hint')).toContain('绑定号不达标或判慢时，会话迁往本组留有余量的合格号')
    expect(zhT2('admin.groupQualityGate.hint')).toContain('严格模式下为“待复测”')
    expect(zhT2('admin.groupQualityGate.tooltip.stale')).toContain('评估数据陈旧或读取失败')
    expect(i18nFor('en').t('admin.groupQualityGate.tooltip.stale')).toContain('stale or could not be read')
  })
})

describe('qualityTooltipTop', () => {
  it('places the panel below, above, or pinned to the viewport', () => {
    expect(qualityTooltipTop({ top: 100, bottom: 120 }, 200, 800)).toBe(126)
    expect(qualityTooltipTop({ top: 700, bottom: 720 }, 200, 800)).toBe(494)
    expect(qualityTooltipTop({ top: 100, bottom: 120 }, 900, 800)).toBe(8)
  })
})
