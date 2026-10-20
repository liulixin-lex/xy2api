/**
 * The "needs attention" list of a share group. It applies the server's
 * preview warning rules (design §7.5) to the member view, so the page shows
 * them without a preview call. Automatic down-weighting shows up once it is
 * deep; ramping accounts are only marked in the traffic table. Pure, so the
 * rules stay testable.
 */

import type { SharePolicyView, ShareWarning } from '@/api/admin/shareScheduling'

export type ShareAttentionCode =
  | 'global_switch_off'
  | 'no_healthy_account'
  | 'top_priority_unhealthy'
  | 'single_account_dominant'
  | 'high_load_factor'
  | 'health_downweighted'
  | 'ramping'

/** A warning as the server sends it, or one read off the member view. */
export interface ShareAttentionItem extends ShareWarning {
  code: ShareAttentionCode | string
  severity: 'warn' | 'info'
  /** Account name, for the message. */
  name?: string
}

/** Thresholds. The first three follow the server's preview rules; the last is to be calibrated against production (待生产校准). */
export const SHARE_ATTENTION_THRESHOLDS = {
  /** One account taking more than this share of new sessions… */
  dominantShare: 0.8,
  /** …while the active priority has at least this many healthy accounts. */
  dominantMinHealthy: 2,
  /** Load factors this high are used to mean "prefer this account". */
  highLoadFactor: 999,
  /** A health factor at or below this asks the administrator to look at the upstream. */
  downweightedFactor: 0.5
} as const

const INFO_CODES = new Set(['high_load_factor', 'ramping'])

function item(code: string, warning: Partial<ShareWarning> = {}, name?: string): ShareAttentionItem {
  return {
    code,
    severity: INFO_CODES.has(code) ? 'info' : 'warn',
    account_id: warning.account_id ?? null,
    priority: warning.priority ?? null,
    value: warning.value ?? null,
    name
  }
}

/** True when automatic down-weighting is deep enough to need a look. */
export function shareDeeplyDownweighted(member: { health_factor: number | null }): boolean {
  return member.health_factor !== null && member.health_factor <= SHARE_ATTENTION_THRESHOLDS.downweightedFactor
}

/**
 * Warnings of the group as it is: the preview rules applied to the member
 * view (global switch only for share groups), plus accounts deeply
 * down-weighted for health.
 */
export function shareAttention(view: SharePolicyView, globalEnabled: boolean | null): ShareAttentionItem[] {
  const t = SHARE_ATTENTION_THRESHOLDS
  const members = view.members
  const items: ShareAttentionItem[] = []

  if (view.scheduler === 'share' && globalEnabled === false) items.push(item('global_switch_off'))

  if (members.length > 0) {
    const top = Math.min(...members.map(m => m.priority))
    if (view.active_priority === null) items.push(item('no_healthy_account'))
    else if (view.active_priority !== top) items.push(item('top_priority_unhealthy', { priority: top, value: view.active_priority }))
  }

  const healthyActive = members.filter(m => m.healthy && m.priority === view.active_priority).length
  if (healthyActive >= t.dominantMinHealthy) {
    for (const m of members) {
      if (m.expected_share > t.dominantShare) items.push(item('single_account_dominant', { account_id: m.account_id, value: m.expected_share }, m.name))
    }
  }
  for (const m of members) {
    if (shareDeeplyDownweighted(m)) items.push(item('health_downweighted', { account_id: m.account_id, value: m.health_factor }, m.name))
  }
  for (const m of members) {
    if (m.effective_load_factor >= t.highLoadFactor) items.push(item('high_load_factor', { account_id: m.account_id, value: m.effective_load_factor }, m.name))
  }

  return items.sort((a, b) => (a.severity === b.severity ? 0 : a.severity === 'warn' ? -1 : 1))
}

/** Attaches account names to the server's preview warnings. */
export function namedWarnings(warnings: ShareWarning[], names: Map<number, string>): ShareAttentionItem[] {
  return warnings.map(w => item(w.code, w, w.account_id !== null ? names.get(w.account_id) : undefined))
}
