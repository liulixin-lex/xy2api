import type {
  GroupQualityDimensionState,
  GroupQualityGateAccountState,
  GroupQualityGateAccountStatus,
  GroupQualityGateGroupStatus,
  GroupQualityGatePolicies,
  GroupQualityGateStatus,
  GroupQualityLatencyHold
} from '@/types/groupQualityGate'

type Translate = (key: string, params?: Record<string, string | number>) => string
type Clock = (iso: string) => string
const prefix = 'admin.groupQualityGate.'

/** 账号在一个开启门槛的分组里的评估结果；分组带着本组标准与本组构成 e_G。 */
export interface AccountGroupQuality {
  kind: GroupQualityGateAccountState
  account: GroupQualityGateAccountStatus
  group: GroupQualityGateGroupStatus
  stale: boolean
}

/** 策略与状态分别读取；失败标志只表示最近一次读取失败。 */
export interface GroupQualitySources {
  policies: GroupQualityGatePolicies | null
  policiesFailed: boolean
  status: GroupQualityGateStatus | null
  statusFailed: boolean
}

export type GroupQualityGateView =
  | { kind: 'loading' | 'off' | 'pending' | 'failed' }
  | { kind: 'evaluated'; group: GroupQualityGateGroupStatus }

/**
 * 分组的门槛显示状态，开没开以策略的 v3 为准：非 OpenAI 或策略关 → off（也覆盖刚关、状态还没更新的情况）；
 * 读取失败 → failed；策略开但状态里还没有这个组（刚开、等下一轮评估）→ pending。
 * 评估器与 status 只看启用的分组，停用分组的门槛不生效，按 off 显示，免得一直“等待评估”。
 */
export function groupQualityGateView(
  group: { id: number; platform: string; status?: string },
  sources: GroupQualitySources
): GroupQualityGateView {
  if (group.platform !== 'openai' || group.status === 'inactive') return { kind: 'off' }
  if (sources.policiesFailed) return { kind: 'failed' }
  if (!sources.policies) return { kind: 'loading' }
  if (!sources.policies.groups[String(group.id)]?.v3?.enabled) return { kind: 'off' }
  if (sources.statusFailed) return { kind: 'failed' }
  if (!sources.status) return { kind: 'loading' }
  const evaluated = sources.status.groups.find(item => item.group_id === group.id && item.enabled)
  return evaluated ? { kind: 'evaluated', group: evaluated } : { kind: 'pending' }
}

/** 评估结果之外的两种中性状态：等待评估、读取失败。 */
export type GroupQualityNoticeKind = 'pending' | 'failed'
export type GroupQualityCell = AccountGroupQuality | { kind: GroupQualityNoticeKind }

/** accountId → groupId → 门槛状态。只收开启门槛的分组。 */
export function buildAccountQualityIndex(status: GroupQualityGateStatus | null): Map<number, Map<number, AccountGroupQuality>> {
  const index = new Map<number, Map<number, AccountGroupQuality>>()
  for (const group of status?.groups ?? []) {
    if (!group.enabled) continue
    for (const account of group.accounts) {
      let byGroup = index.get(account.account_id)
      if (!byGroup) index.set(account.account_id, (byGroup = new Map()))
      byGroup.set(group.group_id, { kind: account.status, account, group, stale: status!.stale })
    }
  }
  return index
}

/**
 * 账号所在各分组的门槛单元（groupId → 单元），规则同分组列表的门槛列。策略关或还在加载的分组不放进来，
 * 显示普通标签；分组已评估但还没有这个账号（刚加入）时为等待评估。
 */
export function buildAccountQualityCells(sources: GroupQualitySources) {
  const evaluated = buildAccountQualityIndex(sources.status)
  const views = new Map<number, GroupQualityGateView>()
  return (accountId: number, groups: { id: number; platform: string }[]): Map<number, GroupQualityCell> => {
    const cells = new Map<number, GroupQualityCell>()
    for (const group of groups) {
      let view = views.get(group.id)
      if (!view) views.set(group.id, (view = groupQualityGateView(group, sources)))
      if (view.kind === 'pending' || view.kind === 'failed') cells.set(group.id, { kind: view.kind })
      else if (view.kind === 'evaluated') cells.set(group.id, evaluated.get(accountId)?.get(group.id) ?? { kind: 'pending' })
    }
    return cells
  }
}

const KIND_RANK: Record<GroupQualityCell['kind'], number> = { failing: 0, retest: 1, failed: 2, pending: 3, unknown: 4, pinned: 5, passing: 6 }

/** 有问题的分组排前面，免得账号列表只显示前几个标签时把不达标藏进 +N；同档保持原顺序。 */
export function orderGroupsByQuality<T extends { id: number }>(groups: T[], quality?: Map<number, GroupQualityCell>): T[] {
  if (!quality?.size) return groups
  const rank = (group: T) => {
    const kind = quality.get(group.id)?.kind
    return kind ? KIND_RANK[kind] : 7
  }
  return groups.map((group, order) => ({ group, order, rank: rank(group) }))
    .sort((a, b) => a.rank - b.rank || a.order - b.order)
    .map(item => item.group)
}

/**
 * 与全局 badge-success / danger / warning / gray 同一套色板。未知与等待评估同为灰色：
 * 0 档里的未知号照常参与调度，但不能看起来像达标。
 */
export const GROUP_QUALITY_BADGE_CLASSES: Record<GroupQualityCell['kind'], string> = {
  passing: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  unknown: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-300',
  retest: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400',
  failing: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
  pinned: 'bg-sky-100 text-sky-700 dark:bg-sky-900/30 dark:text-sky-400',
  pending: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-300',
  failed: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-300'
}

export const isGroupQualityNotice = (cell: GroupQualityCell): cell is { kind: GroupQualityNoticeKind } =>
  cell.kind === 'pending' || cell.kind === 'failed'

/** formatDateTime options for hh:mm. */
export const QUALITY_CLOCK: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hour12: false }

/** 标签上的状态文字：达标 / 未知 / 待复测 / 不达标 / 固定 / 等待评估 / 读取失败。 */
export function groupQualityStateLabel(cell: GroupQualityCell, t: Translate): string {
  return t(prefix + 'state.' + cell.kind)
}

export const formatQualityRate = (rate: number) => `${Math.round(rate * 1000) / 10}%`
export const formatQualitySeconds = (ms: number) => `${Math.round(ms / 100) / 10}s`
const formatFactor = (factor: number) => `×${(Math.round(factor * 100) / 100).toFixed(2)}`
const formatInterval = (low: number, high: number) => `${Math.round(low * 1000) / 10}–${formatQualityRate(high)}`

/** 提示面板的纵向位置：下方放得下放下方，否则放上方；两边都放不下时贴住视口底部，且不超出顶部。 */
export function qualityTooltipTop(
  trigger: { top: number; bottom: number },
  height: number,
  viewportHeight: number,
  gap = 6,
  margin = 8
): number {
  const below = trigger.bottom + gap
  if (below + height <= viewportHeight - margin) return below
  const above = trigger.top - gap - height
  if (above >= margin) return above
  return Math.max(margin, viewportHeight - margin - height)
}

export type QualityDimensionKey = 'ttft' | 'error' | 'cache'

export interface QualityDimensionRow {
  key: QualityDimensionKey
  label: string
  state: GroupQualityDimensionState
  stateLabel: string
  /** 首字为预计 P90，错误为失败率，缓存为保持率；没有统计为“—”，首字维 e_G 不可用时为“本组样本不足” */
  value: string
  /** 灰色小字：首字的速度系数，错误与缓存的 Wilson 区间（只展示，不参与判定） */
  detail?: string
  samples: string
  /** 本组标准：≤ X、≤ Y、≥ Z */
  limit: string
}

/**
 * 每维一行：状态、数值、样本数与本组标准。首字维在本组构成 e_G 不可用时显示“本组样本不足”，
 * 不借用其他组的构成；速度系数照常显示，它属于账号本身。
 */
export function qualityDimensionRows(quality: AccountGroupQuality, t: Translate): QualityDimensionRow[] {
  const { account, group } = quality
  const { policy } = group
  const stateLabel = (state: GroupQualityDimensionState) => t(prefix + 'dimension.' + state)
  const none = t(prefix + 'tooltip.noData')
  const { ttft, error, cache } = account
  const ttftValue = group.expected_p90_ms == null
    ? t(prefix + 'tooltip.groupSamplesShort')
    : ttft.value == null ? none : formatQualitySeconds(ttft.value)
  const interval = (low: number | null, high: number | null) => (low != null && high != null ? t(prefix + 'tooltip.interval', { range: formatInterval(low, high) }) : undefined)
  return [
    {
      key: 'ttft',
      label: t(prefix + 'tooltip.ttft'),
      state: ttft.state,
      stateLabel: stateLabel(ttft.state),
      value: ttftValue,
      detail: ttft.factor != null ? t(prefix + 'tooltip.factor', { value: formatFactor(ttft.factor) }) : undefined,
      samples: t(prefix + 'tooltip.ttftSamples', { samples: ttft.samples, sessions: ttft.sessions }),
      limit: `≤ ${policy.max_ttft_p90_seconds}s`
    },
    {
      key: 'error',
      label: t(prefix + 'tooltip.errorRate'),
      state: error.state,
      stateLabel: stateLabel(error.state),
      value: error.value == null ? none : formatQualityRate(error.value),
      detail: interval(error.wilson_low, error.wilson_high),
      samples: `${error.failures}/${error.samples}`,
      limit: `≤ ${formatQualityRate(policy.max_error_rate)}`
    },
    {
      key: 'cache',
      label: t(prefix + 'tooltip.cacheKeep'),
      state: cache.state,
      stateLabel: stateLabel(cache.state),
      value: cache.value == null ? none : formatQualityRate(cache.value),
      detail: interval(cache.wilson_low, cache.wilson_high),
      samples: String(cache.samples),
      limit: `≥ ${formatQualityRate(policy.min_cache_keep_rate)}`
    }
  ]
}

/** 本组构成 e_G 一行：“本组构成 P90 9.1s（87 条）”，不足 50 条时“本组样本不足（12/50 条）”。 */
export function groupCompositionText(group: GroupQualityGateGroupStatus, t: Translate): { text: string; insufficient: boolean } {
  if (group.expected_p90_ms == null) {
    return { text: t(prefix + 'tooltip.groupSamplesInsufficient', { count: group.expected_samples }), insufficient: true }
  }
  return { text: t(prefix + 'tooltip.composition', { value: formatQualitySeconds(group.expected_p90_ms), count: group.expected_samples }), insufficient: false }
}

export interface QualityGroupBlock {
  kind: GroupQualityCell['kind']
  title: string
  strict?: boolean
  /** 一句短语：固定账号的含义，或等待评估、读取失败的下一步 */
  note?: string
  composition?: { text: string; insufficient: boolean }
  rows: QualityDimensionRow[]
  /** 各维坏记忆的到期时刻，只列还在的 */
  badMemory?: string
}

const DIMENSION_KEYS: QualityDimensionKey[] = ['ttft', 'error', 'cache']

/** 悬停面板里的一组：标题“分组 · 状态”、本组构成、三维小表与坏记忆。 */
export function groupQualityBlock(groupName: string, quality: GroupQualityCell, t: Translate, clock: Clock): QualityGroupBlock {
  const title = t(prefix + 'tooltip.title', { group: groupName, state: groupQualityStateLabel(quality, t) })
  if (isGroupQualityNotice(quality)) {
    return { kind: quality.kind, title, note: t(prefix + 'tooltip.' + quality.kind), rows: [] }
  }
  const bad = DIMENSION_KEYS
    .filter(key => quality.account[key].bad_until)
    .map(key => t(prefix + 'tooltip.badMemoryItem', { dimension: t(prefix + 'dimensionName.' + key), time: clock(quality.account[key].bad_until!) }))
  return {
    kind: quality.kind,
    title,
    strict: quality.group.policy.strict || undefined,
    note: quality.kind === 'pinned' ? t(prefix + 'tooltip.pinned') : undefined,
    composition: groupCompositionText(quality.group, t),
    rows: qualityDimensionRows(quality, t),
    ...(bad.length ? { badMemory: t(prefix + 'tooltip.badMemory', { items: bad.join(t(prefix + 'tooltip.separator')) }) } : {})
  }
}

/** 需要关注的状态：不达标、待复测。未知、固定、达标、等待评估、读取失败都不算。 */
export type GroupQualityAttentionKind = 'failing' | 'retest'
export const isGroupQualityAttention = (cell: GroupQualityCell): cell is AccountGroupQuality & { kind: GroupQualityAttentionKind } =>
  cell.kind === 'failing' || cell.kind === 'retest'

/**
 * 判慢让位按账号 × 模型，与分组无关：各组条目里取模型并集与最晚到期。已经到期的不显示，
 * 也不提示“已恢复”——到期只解除后排，门槛状态不变。
 */
export function activeLatencyHold(cells: Iterable<GroupQualityCell> | undefined, now = Date.now()): GroupQualityLatencyHold | null {
  let until = 0
  const models = new Set<string>()
  for (const cell of cells ?? []) {
    if (isGroupQualityNotice(cell)) continue
    const hold = cell.account.latency_hold
    const at = hold ? Date.parse(hold.until) : NaN
    if (!hold || !(at > now)) continue
    until = Math.max(until, at)
    hold.models.forEach(model => models.add(model))
  }
  return until ? { until: new Date(until).toISOString(), models: [...models] } : null
}

export const latencyHoldText = (hold: GroupQualityLatencyHold, t: Translate, clock: Clock) =>
  t(prefix + 'latencyHold.text', { models: hold.models.join(t(prefix + 'tooltip.separator')), time: clock(hold.until) })

/** 分组列小标识的颜色：最严重的需关注状态，与状态标签同一色板；只有判慢让位时为琥珀色时钟；都没有时为灰色。 */
export type GroupQualityIndicatorKind = GroupQualityAttentionKind | 'hold' | 'none'
export const GROUP_QUALITY_INDICATOR_CLASSES: Record<GroupQualityIndicatorKind, string> = {
  failing: 'text-red-600 dark:text-red-400',
  retest: 'text-amber-600 dark:text-amber-400',
  hold: 'text-amber-600 dark:text-amber-400',
  none: 'text-gray-400 hover:text-gray-600 dark:text-dark-400 dark:hover:text-gray-300'
}

export interface QualityPanel {
  indicator: GroupQualityIndicatorKind
  attentionCount: number
  /** 按 orderGroupsByQuality 排好：需关注 → 读取失败 → 等待评估 → 未知 → 固定 → 达标 */
  blocks: QualityGroupBlock[]
  /** 判慢让位（账号级），到期时刻与“到期只解除后排”的说明 */
  latencyHold?: { text: string; note: string }
  /** 评估数据陈旧，整个面板只提示一次 */
  stale?: string
}

/** 分组列门槛面板：只收开启门槛的分组，每组一段；判慢让位与数据陈旧在面板底部各提示一次。 */
export function groupQualityPanel(
  groups: { id: number; name: string }[],
  quality: Map<number, GroupQualityCell>,
  t: Translate,
  clock: Clock,
  now = Date.now()
): QualityPanel | null {
  const panel: QualityPanel = { indicator: 'none', attentionCount: 0, blocks: [] }
  let attention: GroupQualityAttentionKind | null = null
  let stale = false
  for (const group of groups) {
    const cell = quality.get(group.id)
    if (!cell) continue
    if (!isGroupQualityNotice(cell)) stale ||= cell.stale
    if (isGroupQualityAttention(cell)) {
      if (!attention || KIND_RANK[cell.kind] < KIND_RANK[attention]) attention = cell.kind
      panel.attentionCount++
    }
    panel.blocks.push(groupQualityBlock(group.name, cell, t, clock))
  }
  if (!panel.blocks.length) return null
  const hold = activeLatencyHold(quality.values(), now)
  if (hold) panel.latencyHold = { text: latencyHoldText(hold, t, clock), note: t(prefix + 'latencyHold.note') }
  panel.indicator = attention ?? (hold ? 'hold' : 'none')
  if (stale) panel.stale = t(prefix + 'tooltip.stale')
  return panel
}

export interface AccountQualityAlertItem {
  key: QualityDimensionKey | 'latency_hold'
  text: string
  title: string
  tone: 'danger' | 'warning'
}

/**
 * 账号行分组单元格下方的紧凑提示，只在异常时出现：某维在任一门槛分组里当前不达标时列出该维数值
 * （首字取各组预计 P90 最高者），以及正在判慢让位的到期时刻。
 */
export function accountQualityAlertItems(
  quality: Map<number, GroupQualityCell> | undefined,
  t: Translate,
  clock: Clock,
  now = Date.now()
): AccountQualityAlertItem[] {
  const cells = [...(quality?.values() ?? [])].filter((cell): cell is AccountGroupQuality => !isGroupQualityNotice(cell))
  const items: AccountQualityAlertItem[] = []
  for (const key of DIMENSION_KEYS) {
    const failing = cells.filter(cell => cell.account[key].state === 'fail')
    if (!failing.length) continue
    const values = failing.map(cell => cell.account[key].value).filter((value): value is number => value != null)
    const value = values.length ? (key === 'ttft' ? formatQualitySeconds(Math.max(...values)) : formatQualityRate(values[0])) : t(prefix + 'tooltip.noData')
    items.push({
      key,
      text: t(prefix + 'alert.' + key, { value }),
      title: t(prefix + 'alert.title', { dimension: t(prefix + 'dimensionName.' + key), count: failing.length }),
      tone: 'danger'
    })
  }
  const hold = activeLatencyHold(cells, now)
  if (hold) {
    items.push({ key: 'latency_hold', text: t(prefix + 'alert.latencyHold', { time: clock(hold.until) }), title: latencyHoldText(hold, t, clock), tone: 'warning' })
  }
  return items
}

/** 分组列门槛摘要的悬停说明：五类成员数与本组构成，例如“达标 3 · 未知 2 · 待复测 1 · 不达标 1 · 固定 0；本组构成 P90 9.1s（87 条）”。 */
export function groupQualitySummaryTitle(group: GroupQualityGateGroupStatus, t: Translate): string {
  const kinds: GroupQualityGateAccountState[] = ['passing', 'unknown', 'retest', 'failing', 'pinned']
  const counts = kinds.map(kind => `${t(prefix + 'state.' + kind)} ${group.summary[kind]}`).join(' · ')
  return t(prefix + 'summaryTitle', { counts, composition: groupCompositionText(group, t).text })
}
