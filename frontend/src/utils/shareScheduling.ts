/**
 * Share scheduling display helpers. A share group ranks accounts by priority
 * (the lowest number with a healthy account takes new sessions) and splits
 * new sessions inside that priority by load factor.
 */

import type { ShareMember } from '@/api/admin/shareScheduling'

export const SHARE_MIN_LOAD_FACTOR = 1
export const SHARE_MAX_LOAD_FACTOR = 10_000

export interface SharePrioritySection {
  priority: number
  /** This priority takes new sessions now. */
  active: boolean
  members: ShareMember[]
}

/** Groups members by priority, lowest first; members keep a stable order inside. */
export function sharePrioritySections(members: ShareMember[], activePriority: number | null): SharePrioritySection[] {
  const byPriority = new Map<number, ShareMember[]>()
  for (const member of members) {
    const list = byPriority.get(member.priority) ?? []
    list.push(member)
    byPriority.set(member.priority, list)
  }
  return [...byPriority.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([priority, list]) => ({
      priority,
      active: priority === activePriority,
      members: list.slice().sort((a, b) =>
        b.expected_share - a.expected_share ||
        b.share_if_healthy - a.share_if_healthy ||
        a.account_id - b.account_id)
    }))
}

interface ShareErrorLike {
  status?: number
  reason?: string
}

function shareError(err: unknown): ShareErrorLike {
  return err && typeof err === 'object' ? (err as ShareErrorLike) : {}
}

export function isShareConflict(err: unknown): boolean {
  const e = shareError(err)
  return e.status === 409 || e.reason === 'SHARE_SCHEDULING_VERSION_CONFLICT'
}

export function isShareUnavailable(err: unknown): boolean {
  const e = shareError(err)
  return e.status === 503 || e.reason === 'SHARE_SCHEDULING_UNAVAILABLE'
}

export function formatSharePercent(value: number | null | undefined, digits = 1): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '-'
  return `${(value * 100).toFixed(digits)}%`
}

/** Formats milliseconds as "350 ms", "1.2 s" or "3.5 min". */
export function formatShareMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return '-'
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} min`
  return `${(ms / 3_600_000).toFixed(1)} h`
}
