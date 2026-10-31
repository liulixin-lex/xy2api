import type {
  GroupQualityAccountMetrics,
  GroupQualityGateAccountStatus,
  GroupQualityGateGroupStatus,
  GroupQualityGatePolicies,
  GroupQualityGateStatus,
  GroupQualityMemberMetrics
} from '@/types/groupQualityGate'
import { QUALITY_GATE_DEFAULTS } from '@/views/admin/groupsQualityGate'

type Translate = (key: string, params?: Record<string, string | number>) => string
const prefix = 'admin.groupQualityGate.'

/** 续轮零缓存率超过 30% 的账号不算快档；样本不足 20 时后端不看，列表也不标红。 */
export const ZERO_CACHE_ALERT_RATE = 0.3
export const ZERO_CACHE_MIN_SAMPLES = 20

/** 保底保留单独成档：它处于熔断，但为保住本组最少可用账号仍参与调度。 */
export type GroupQualityBadgeKind = 'eligible' | 'ejected' | 'trial' | 'guard_held'

export interface AccountGroupQuality {
  kind: GroupQualityBadgeKind
  status: GroupQualityGateAccountStatus
  maxErrorRate: number
  maxWarmTtftMs: number
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
 * 分组的门槛显示状态，开没开以策略为准：非 OpenAI 或策略关 → off（也覆盖刚关、状态还没更新的情况）；
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
  if (!sources.policies.groups[String(group.id)]?.enabled) return { kind: 'off' }
  if (sources.statusFailed) return { kind: 'failed' }
  if (!sources.status) return { kind: 'loading' }
  const evaluated = sources.status.groups.find(item => item.group_id === group.id && item.enabled)
  return evaluated ? { kind: 'evaluated', group: evaluated } : { kind: 'pending' }
}

/** 评估结果之外的两种中性状态：等待评估、读取失败。 */
export type GroupQualityNoticeKind = 'pending' | 'failed'
export type GroupQualityCell = AccountGroupQuality | { kind: GroupQualityNoticeKind }

/** accountId → groupId → 门槛状态。只收开启门槛的分组；上限取策略，零值或缺省取默认。 */
export function buildAccountQualityIndex(
  status: GroupQualityGateStatus | null,
  policies: GroupQualityGatePolicies | null
): Map<number, Map<number, AccountGroupQuality>> {
  const index = new Map<number, Map<number, AccountGroupQuality>>()
  for (const group of status?.groups ?? []) {
    if (!group.enabled) continue
    const policy = policies?.groups[String(group.group_id)]
    const maxErrorRate = policy && policy.max_error_rate > 0 ? policy.max_error_rate : QUALITY_GATE_DEFAULTS.max_error_rate
    const maxWarmTtftMs = policy && policy.max_warm_ttft_p90_ms > 0 ? policy.max_warm_ttft_p90_ms : QUALITY_GATE_DEFAULTS.max_warm_ttft_p90_ms
    for (const account of group.accounts) {
      let byGroup = index.get(account.account_id)
      if (!byGroup) index.set(account.account_id, (byGroup = new Map()))
      byGroup.set(group.group_id, {
        kind: account.guard_held ? 'guard_held' : account.state,
        status: account,
        maxErrorRate,
        maxWarmTtftMs,
        stale: status!.stale
      })
    }
  }
  return index
}

/**
 * 账号所在各分组的门槛单元（groupId → 单元），规则同分组列表的门槛列。策略关或还在加载的分组不放进来，
 * 显示普通标签；分组已评估但还没有这个账号（刚加入）时为等待评估。
 */
export function buildAccountQualityCells(sources: GroupQualitySources) {
  const evaluated = buildAccountQualityIndex(sources.status, sources.policies)
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

const KIND_RANK: Record<GroupQualityCell['kind'], number> = { ejected: 0, guard_held: 1, trial: 2, failed: 3, pending: 4, eligible: 5 }

/** 有问题的分组排前面，免得账号列表只显示前几个标签时把熔断藏进 +N；同档保持原顺序。 */
export function orderGroupsByQuality<T extends { id: number }>(groups: T[], quality?: Map<number, GroupQualityCell>): T[] {
  if (!quality?.size) return groups
  const rank = (group: T) => {
    const kind = quality.get(group.id)?.kind
    return kind ? KIND_RANK[kind] : 6
  }
  return groups.map((group, order) => ({ group, order, rank: rank(group) }))
    .sort((a, b) => a.rank - b.rank || a.order - b.order)
    .map(item => item.group)
}

/** 与全局 badge-success / danger / warning / gray 同一套色板。 */
export const GROUP_QUALITY_BADGE_CLASSES: Record<GroupQualityCell['kind'], string> = {
  eligible: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  ejected: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
  trial: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400',
  guard_held: 'bg-sky-100 text-sky-700 dark:bg-sky-900/30 dark:text-sky-400',
  pending: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-300',
  failed: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-dark-300'
}

export const isGroupQualityNotice = (cell: GroupQualityCell): cell is { kind: GroupQualityNoticeKind } =>
  cell.kind === 'pending' || cell.kind === 'failed'

/** formatDateTime options for the hh:mm ejection end. */
export const QUALITY_CLOCK: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hour12: false }

const STATE_KEYS: Record<GroupQualityCell['kind'], string> = {
  eligible: 'eligible', ejected: 'ejected', trial: 'trial', guard_held: 'guardHeld', pending: 'pending', failed: 'failed'
}

/** 标签上的状态文字：合格 / 熔断至 hh:mm / 试用 / 保底保留 / 等待评估 / 读取失败。 */
export function groupQualityStateLabel(cell: GroupQualityCell, t: Translate, clock: (iso: string) => string): string {
  if (cell.kind === 'ejected' && cell.status.until) {
    return t(prefix + 'state.ejectedUntil', { time: clock(cell.status.until) })
  }
  return t(prefix + 'state.' + STATE_KEYS[cell.kind])
}

export const formatQualityRate = (rate: number) => `${Math.round(rate * 1000) / 10}%`
export const formatQualitySeconds = (ms: number) => `${Math.round(ms / 100) / 10}s`
const formatHeadroom = (headroom: number) => `${Math.round(headroom * 100)}%`

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

export interface QualityTooltipRow {
  key: 'error_rate' | 'warm_ttft'
  label: string
  value: string
  /** 灰色小字：未达判定条件等 */
  note?: string
  limit: string
  over: boolean
}

export interface QualityTooltipBlock {
  title: string
  /** 最多一句短语：固定、保底保留，或等待评估、读取失败的下一步 */
  note?: string
  reasons: string[]
  rows: QualityTooltipRow[]
  /** “实测”表头旁的小字：熔断中改看完整窗口时标“熔断时窗口” */
  measuredNote?: string
  /** 只在超线或接近上限（余量 < 20%，即恢复线以内）时出现 */
  headroom?: { text: string; tone: 'danger' | 'warning' }
  stale?: string
}

/** 余量低于恢复线（上限的 80%）时提示，正好对应“恢复需降到上限 80% 以下”。 */
export const QUALITY_HEADROOM_WARN = 0.2

/**
 * 悬停说明里的一组：标题“分组 · 状态”、原因标签、指标 | 实测 | 上限 小表，余量只在超线或接近时出现。
 * 合格、试用取成员 metrics 与后端余量（本组判定口径）。熔断中（含保底保留）成员 metrics 只计熔断后的数据，
 * 刚熔断时多半样本不足，和原因对不上，所以改看账号的完整窗口（触发熔断的数据），余量也按它算。
 */
export function groupQualityTooltip(
  groupName: string,
  quality: GroupQualityCell,
  t: Translate,
  clock: (iso: string) => string,
  fullWindow?: GroupQualityAccountMetrics
): QualityTooltipBlock {
  const title = (state: string) => t(prefix + 'tooltip.title', { group: groupName, state })
  if (isGroupQualityNotice(quality)) {
    return { title: title(groupQualityStateLabel(quality, t, clock)), note: t(prefix + 'tooltip.' + quality.kind), reasons: [], rows: [] }
  }
  const { status } = quality
  let state = groupQualityStateLabel(quality, t, clock)
  if (status.state === 'ejected' && status.strikes > 1) state += t(prefix + 'tooltip.strikes', { count: status.strikes })
  const ejectionWindow = status.state === 'ejected' ? fullWindow : undefined
  const metrics = ejectionWindow ? decisionMetricsFromWindow(ejectionWindow) : status.metrics
  const headroom = ejectionWindow ? qualityHeadroom(metrics, quality) : status.headroom
  const errorRow: QualityTooltipRow = { key: 'error_rate', label: t(prefix + 'tooltip.errorRate'), value: t(prefix + 'tooltip.noData'), limit: formatQualityRate(quality.maxErrorRate), over: false }
  if (metrics?.error_rate != null) {
    errorRow.value = formatQualityRate(metrics.error_rate)
    errorRow.over = metrics.error_rate > quality.maxErrorRate
  } else if (metrics && metrics.calls > 0) {
    errorRow.value = formatQualityRate(metrics.soft_failures / metrics.calls)
    errorRow.note = t(prefix + 'tooltip.belowThreshold')
  }
  const ttftRow: QualityTooltipRow = { key: 'warm_ttft', label: t(prefix + 'tooltip.warmTtft'), value: t(prefix + 'tooltip.noData'), limit: formatQualitySeconds(quality.maxWarmTtftMs), over: false }
  if (metrics?.warm_ttft_p90_ms != null) {
    ttftRow.value = formatQualitySeconds(metrics.warm_ttft_p90_ms)
    ttftRow.over = metrics.warm_ttft_p90_ms > quality.maxWarmTtftMs
  } else if (metrics && metrics.warm_samples > 0) {
    ttftRow.value = t(prefix + 'tooltip.notEnoughSamples')
  }
  const block: QualityTooltipBlock = {
    title: title(state),
    note: status.pinned ? t(prefix + 'tooltip.pinned') : quality.kind === 'guard_held' ? t(prefix + 'tooltip.guardHeld') : undefined,
    reasons: status.reasons.map(reason => t(prefix + 'reason.' + reason)),
    rows: [errorRow, ttftRow],
    measuredNote: ejectionWindow ? t(prefix + 'tooltip.ejectionWindow') : undefined
  }
  if (headroom != null && headroom < QUALITY_HEADROOM_WARN) {
    block.headroom = { text: t(prefix + 'tooltip.headroom', { value: formatHeadroom(headroom) }), tone: headroom < 0 ? 'danger' : 'warning' }
  }
  if (quality.stale) block.stale = t(prefix + 'tooltip.stale')
  return block
}

/** 与后端判定条件一致：错误率要调用 ≥ 20 且软失败 ≥ 5，续轮首字要样本 ≥ 10，才算“超线”。 */
export const QUALITY_MIN_CALLS = 20
export const QUALITY_MIN_SOFT_FAILURES = 5
export const QUALITY_MIN_WARM_SAMPLES = 10

export interface QualityLimits {
  maxErrorRate: number
  maxWarmTtftMs: number
}

/** 完整窗口换成判定口径：样本未达判定条件的错误率、首字记为 null，与成员 metrics 一致。 */
export function decisionMetricsFromWindow(metrics: GroupQualityAccountMetrics): GroupQualityMemberMetrics {
  const errorReady = metrics.calls >= QUALITY_MIN_CALLS && metrics.soft_failures >= QUALITY_MIN_SOFT_FAILURES
  return {
    calls: metrics.calls,
    soft_failures: metrics.soft_failures,
    error_rate: errorReady ? metrics.error_rate : null,
    warm_samples: metrics.warm_samples,
    warm_ttft_p90_ms: metrics.warm_samples >= QUALITY_MIN_WARM_SAMPLES ? metrics.warm_ttft_p90_ms : null
  }
}

/** 与后端同一算法：非 null 指标里最小的 1 − 实测 / 上限，全为 null 时为 null。 */
function qualityHeadroom(metrics: GroupQualityMemberMetrics, limits: QualityLimits): number | null {
  const values: number[] = []
  if (metrics.error_rate != null) values.push(1 - metrics.error_rate / limits.maxErrorRate)
  if (metrics.warm_ttft_p90_ms != null) values.push(1 - metrics.warm_ttft_p90_ms / limits.maxWarmTtftMs)
  return values.length ? Math.min(...values) : null
}

/** 账号所在已评估分组里最严的上限，用来给账号行的紧凑指标着色；没有已评估分组时返回 undefined。 */
export function strictestQualityLimits(cells: Map<number, GroupQualityCell> | undefined): QualityLimits | undefined {
  let limits: QualityLimits | undefined
  for (const cell of cells?.values() ?? []) {
    if (isGroupQualityNotice(cell)) continue
    limits = limits
      ? { maxErrorRate: Math.min(limits.maxErrorRate, cell.maxErrorRate), maxWarmTtftMs: Math.min(limits.maxWarmTtftMs, cell.maxWarmTtftMs) }
      : { maxErrorRate: cell.maxErrorRate, maxWarmTtftMs: cell.maxWarmTtftMs }
  }
  return limits
}

export interface AccountQualityMetricItem {
  key: 'error_rate' | 'warm_ttft' | 'zero_cache'
  text: string
  title: string
  alert: boolean
}

/**
 * 账号行的紧凑指标（完整窗口）：只列有样本的项，完整名称、时间窗与样本数在 title。
 * 错误率与首字在样本足够且超过所在分组最严上限时着色；零缓存率在样本 ≥ 20 且超过 30% 时着色。
 */
export function accountQualityMetricItems(metrics: GroupQualityAccountMetrics, t: Translate, limits?: QualityLimits): AccountQualityMetricItem[] {
  const items: AccountQualityMetricItem[] = []
  if (metrics.calls > 0) {
    items.push({
      key: 'error_rate',
      text: t(prefix + 'metrics.errorRate', { value: formatQualityRate(metrics.error_rate) }),
      title: t(prefix + 'metrics.errorRateTitle', { calls: metrics.calls, failures: metrics.soft_failures }),
      alert: !!limits && metrics.calls >= QUALITY_MIN_CALLS && metrics.soft_failures >= QUALITY_MIN_SOFT_FAILURES && metrics.error_rate > limits.maxErrorRate
    })
  }
  if (metrics.warm_samples > 0) {
    items.push({
      key: 'warm_ttft',
      text: t(prefix + 'metrics.warmTtft', { value: formatQualitySeconds(metrics.warm_ttft_p90_ms) }),
      title: t(prefix + 'metrics.warmTtftTitle', { count: metrics.warm_samples }),
      alert: !!limits && metrics.warm_samples >= QUALITY_MIN_WARM_SAMPLES && metrics.warm_ttft_p90_ms > limits.maxWarmTtftMs
    })
  }
  if (metrics.zero_cache_samples > 0) {
    items.push({
      key: 'zero_cache',
      text: t(prefix + 'metrics.zeroCache', { value: formatQualityRate(metrics.warm_zero_cache_rate) }),
      title: t(prefix + 'metrics.zeroCacheTitle', { count: metrics.zero_cache_samples }),
      alert: metrics.zero_cache_samples >= ZERO_CACHE_MIN_SAMPLES && metrics.warm_zero_cache_rate > ZERO_CACHE_ALERT_RATE
    })
  }
  return items
}
