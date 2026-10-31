import type {
  GroupQualityAccountMetrics,
  GroupQualityGateAccountStatus,
  GroupQualityGateGroupStatus,
  GroupQualityGatePolicies,
  GroupQualityGateStatus
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

/** 悬停说明：状态、原因、实测值与上限、余量。实测值是账号完整窗口的指标，所有分组共用。 */
export function groupQualityTooltip(
  groupName: string,
  quality: GroupQualityCell,
  metrics: GroupQualityAccountMetrics | undefined,
  t: Translate,
  clock: (iso: string) => string
): string[] {
  if (isGroupQualityNotice(quality)) return [t(prefix + 'tooltip.' + quality.kind, { group: groupName })]
  const { status } = quality
  const lines = [t(prefix + 'tooltip.state', { group: groupName, state: groupQualityStateLabel(quality, t, clock) })]
  if (status.pinned) lines.push(t(prefix + 'tooltip.pinned'))
  if (quality.kind === 'guard_held') lines.push(t(prefix + 'tooltip.guardHeld'))
  if (status.state === 'ejected' && status.strikes > 0) lines.push(t(prefix + 'tooltip.strikes', { count: status.strikes }))
  if (status.reasons.length) {
    const reasons = status.reasons.map(reason => t(prefix + 'reason.' + reason)).join(t(prefix + 'listSeparator'))
    lines.push(t(prefix + 'tooltip.reasons', { reasons }))
  }
  const errorLimit = formatQualityRate(quality.maxErrorRate)
  const ttftLimit = formatQualitySeconds(quality.maxWarmTtftMs)
  lines.push(metrics && metrics.calls > 0
    ? t(prefix + 'tooltip.errorRate', { value: formatQualityRate(metrics.error_rate), limit: errorLimit })
    : t(prefix + 'tooltip.errorRateNoData', { limit: errorLimit }))
  lines.push(metrics && metrics.warm_samples > 0
    ? t(prefix + 'tooltip.warmTtft', { value: formatQualitySeconds(metrics.warm_ttft_p90_ms), limit: ttftLimit })
    : t(prefix + 'tooltip.warmTtftNoData', { limit: ttftLimit }))
  lines.push(status.headroom == null
    ? t(prefix + 'tooltip.headroomNoData')
    : t(prefix + 'tooltip.headroom', { value: formatHeadroom(status.headroom) }))
  if (quality.stale) lines.push(t(prefix + 'tooltip.stale'))
  return lines
}

export interface AccountQualityMetricItem {
  key: 'error_rate' | 'warm_ttft' | 'zero_cache'
  text: string
  title: string
  alert: boolean
}

/** 行内三项指标（紧凑写法，完整名称、时间窗与样本数在 title）；没有样本时显示“—”。零缓存率只在样本足够且超过 30% 时标红。 */
export function accountQualityMetricItems(metrics: GroupQualityAccountMetrics, t: Translate): AccountQualityMetricItem[] {
  const none = '—'
  return [
    {
      key: 'error_rate',
      text: t(prefix + 'metrics.errorRate', { value: metrics.calls > 0 ? formatQualityRate(metrics.error_rate) : none }),
      title: t(prefix + 'metrics.errorRateTitle', { calls: metrics.calls, failures: metrics.soft_failures }),
      alert: false
    },
    {
      key: 'warm_ttft',
      text: t(prefix + 'metrics.warmTtft', { value: metrics.warm_samples > 0 ? formatQualitySeconds(metrics.warm_ttft_p90_ms) : none }),
      title: t(prefix + 'metrics.warmTtftTitle', { count: metrics.warm_samples }),
      alert: false
    },
    {
      key: 'zero_cache',
      text: t(prefix + 'metrics.zeroCache', { value: metrics.zero_cache_samples > 0 ? formatQualityRate(metrics.warm_zero_cache_rate) : none }),
      title: t(prefix + 'metrics.zeroCacheTitle', { count: metrics.zero_cache_samples }),
      alert: metrics.zero_cache_samples >= ZERO_CACHE_MIN_SAMPLES && metrics.warm_zero_cache_rate > ZERO_CACHE_ALERT_RATE
    }
  ]
}
