/**
 * The "needs attention" list of a share group. It applies the server's
 * preview warning rules (design §7.5) to the member view, so the page shows
 * them without a preview call, and adds a suggestion for accounts that fail
 * often. Pure, so the rules stay testable.
 */

import type { SharePolicyView, ShareWarning } from '@/api/admin/shareScheduling'

export type ShareAttentionCode =
  | 'global_switch_off'
  | 'no_healthy_account'
  | 'top_priority_unhealthy'
  | 'single_account_dominant'
  | 'high_load_factor'
  | 'high_failure'

/** A warning as the server sends it, or one read off the member view. */
export interface ShareAttentionItem extends ShareWarning {
  code: ShareAttentionCode | string
  severity: 'warn' | 'info'
  /** Account name, for the message. */
  name?: string
}

/** Thresholds. The first three follow the server's preview rules; the failure rule is to be calibrated against production (待生产校准). */
export const SHARE_ATTENTION_THRESHOLDS = {
  /** One account taking more than this share of new sessions… */
  dominantShare: 0.8,
  /** …while the active priority has at least this many healthy accounts. */
  dominantMinHealthy: 2,
  /** Load factors this high are used to mean "prefer this account". */
  highLoadFactor: 999,
  /** Error rate of the last hour (all groups) worth a suggestion. */
  failureRate: 0.1,
  /** Requests plus errors needed before the error rate counts. */
  failureMinTries: 20
} as const

const INFO_CODES = new Set(['high_load_factor'])

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

/** True when the account's last hour of errors is worth a suggestion. */
export function shareFailsOften(member: { recent_requests_1h: number; recent_errors_1h: number; recent_error_rate_1h: number | null }): boolean {
  const t = SHARE_ATTENTION_THRESHOLDS
  return member.recent_error_rate_1h !== null &&
    member.recent_requests_1h + member.recent_errors_1h >= t.failureMinTries &&
    member.recent_error_rate_1h >= t.failureRate
}

/**
 * Warnings of the group as it is: the preview rules applied to the member
 * view (global switch only for share groups), plus frequent failures.
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
    if (shareFailsOften(m)) items.push(item('high_failure', { account_id: m.account_id, value: m.recent_error_rate_1h }, m.name))
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
