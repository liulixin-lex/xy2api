import type {
  GroupQualityAccountMetrics,
  GroupQualityGateAccountStatus,
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

const KIND_RANK: Record<GroupQualityBadgeKind, number> = { ejected: 0, guard_held: 1, trial: 2, eligible: 3 }

/** 有问题的分组排前面，免得账号列表只显示前几个标签时把熔断藏进 +N；同档保持原顺序。 */
export function orderGroupsByQuality<T extends { id: number }>(groups: T[], quality?: Map<number, AccountGroupQuality>): T[] {
  if (!quality?.size) return groups
  const rank = (group: T) => {
    const kind = quality.get(group.id)?.kind
    return kind ? KIND_RANK[kind] : 4
  }
  return groups.map((group, order) => ({ group, order, rank: rank(group) }))
    .sort((a, b) => a.rank - b.rank || a.order - b.order)
    .map(item => item.group)
}

/** 与全局 badge-success / danger / warning 同一套色板。 */
export const GROUP_QUALITY_BADGE_CLASSES: Record<GroupQualityBadgeKind, string> = {
  eligible: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  ejected: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
  trial: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400',
  guard_held: 'bg-sky-100 text-sky-700 dark:bg-sky-900/30 dark:text-sky-400'
}

/** formatDateTime options for the hh:mm ejection end. */
export const QUALITY_CLOCK: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hour12: false }

const STATE_KEYS: Record<GroupQualityBadgeKind, string> = { eligible: 'eligible', ejected: 'ejected', trial: 'trial', guard_held: 'guardHeld' }

/** 标签上的状态文字：合格 / 熔断至 hh:mm / 试用 / 保底保留。 */
export function groupQualityStateLabel(quality: AccountGroupQuality, t: Translate, clock: (iso: string) => string): string {
  if (quality.kind === 'ejected' && quality.status.until) {
    return t(prefix + 'state.ejectedUntil', { time: clock(quality.status.until) })
  }
  return t(prefix + 'state.' + STATE_KEYS[quality.kind])
}

export const formatQualityRate = (rate: number) => `${Math.round(rate * 1000) / 10}%`
export const formatQualitySeconds = (ms: number) => `${Math.round(ms / 100) / 10}s`
const formatHeadroom = (headroom: number) => `${Math.round(headroom * 100)}%`

/** 悬停说明：状态、原因、实测值与上限、余量。实测值是账号完整窗口的指标，所有分组共用。 */
export function groupQualityTooltip(
  groupName: string,
  quality: AccountGroupQuality,
  metrics: GroupQualityAccountMetrics | undefined,
  t: Translate,
  clock: (iso: string) => string
): string[] {
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
  lines.push(t(prefix + 'tooltip.headroom', { value: formatHeadroom(status.headroom) }))
  if (quality.stale) lines.push(t(prefix + 'tooltip.stale'))
  return lines
}

export interface AccountQualityMetricItem {
  key: 'error_rate' | 'warm_ttft' | 'zero_cache'
  text: string
  title: string
  alert: boolean
}

/** 行内三项指标；没有样本时显示“—”。零缓存率只在样本足够且超过 30% 时标红。 */
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
