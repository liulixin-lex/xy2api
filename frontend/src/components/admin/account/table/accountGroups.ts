/**
 * The accounts table's groups column (spec 4.3): which pills a row shows, their text, and each pill's dot.
 */
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'

/** A pill's dot, in the spec's order of precedence. `none`: no quality data (not an OpenAI account): no dot. */
export type GroupDotState = 'stopped' | 'exempt' | 'back' | 'trial' | 'unknown' | 'ok'

/** What the dot derivation reads from the account's quality summary. */
export type AccountQualityView = Pick<BackendAccountSummary, 'group_status' | 'stop' | 'rear' | 'trial' | 'exemptions'>

const STATUS_DOTS = { rear: 'back', trial: 'trial', pass: 'ok', unknown: 'unknown' } as const

/**
 * The dot for one group, first match wins (H.11):
 * 1 stopped: the whole account takes no part in scheduling (`stop` set);
 * 2 exempt: an exemption for this group or for every group (group_id 0);
 * 3 the backend's identity of the group (`group_status`): rear → back, trial, pass → ok, unknown; a group the summary
 *   does not name has no data, which is unknown.
 */
export function groupDotState(quality: AccountQualityView, groupId: number): GroupDotState {
  if (quality.stop) return 'stopped'
  if (quality.exemptions.some((exemption) => exemption.group_id === groupId || exemption.group_id === 0)) return 'exempt'
  const status = quality.group_status?.[String(groupId)]
  return status && status in STATUS_DOTS ? STATUS_DOTS[status] : 'unknown'
}

const SEVERITY: Record<GroupDotState, number> = { ok: 1, exempt: 2, unknown: 3, trial: 4, back: 5, stopped: 6 }

/** The worst of several dots (the "+N" pill shows the worst of the groups folded into it). */
export function worstDotState(states: GroupDotState[]): GroupDotState | null {
  return states.reduce<GroupDotState | null>((worst, state) => (worst === null || SEVERITY[state] > SEVERITY[worst] ? state : worst), null)
}

/** Up to nine pills: with more than nine groups the ninth cell is "+N" for the rest. */
export const MAX_PILLS = 9

export function splitPills<T>(groups: T[]): { shown: T[]; rest: T[] } {
  if (groups.length <= MAX_PILLS) return { shown: groups, rest: [] }
  return { shown: groups.slice(0, MAX_PILLS - 1), rest: groups.slice(MAX_PILLS - 1) }
}

/** Groups in the order of the groups page (sort_order, then id): a pill keeps its place whatever its state. */
export function orderGroups<T extends { id: number; sort_order?: number | null }>(groups: T[]): T[] {
  return [...groups].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0) || a.id - b.id)
}

