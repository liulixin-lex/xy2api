/**
 * The accounts table's groups column (spec 4.3): which pills a row shows, their text, and each pill's dot.
 */
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'

/** A pill's dot, in the spec's order of precedence. `none`: no quality data (not an OpenAI account): no dot. */
export type GroupDotState = 'stopped' | 'exempt' | 'back' | 'observe' | 'ok'

/** What the dot derivation reads from the account's quality summary. */
export type AccountQualityView = Pick<BackendAccountSummary, 'status' | 'stop' | 'substandard' | 'exemptions'>

/**
 * The dot for one group, first match wins:
 * 1 stopped: the whole account takes no part in scheduling (`stop` set);
 * 2 exempt: an exemption for this group or for every group (group_id 0);
 * 3 back: a substandard record of this group that is in effect (the backend's `effective`: the account really is
 *   at the back of the group's order);
 * 4 observe: the account is still observed (too few samples);
 * 5 ok.
 */
export function groupDotState(quality: AccountQualityView, groupId: number): GroupDotState {
  if (quality.stop) return 'stopped'
  if (quality.exemptions.some((exemption) => exemption.group_id === groupId || exemption.group_id === 0)) return 'exempt'
  if (quality.substandard.some((item) => item.group_id === groupId && item.effective)) return 'back'
  if (quality.status === 'observing') return 'observe'
  return 'ok'
}

const SEVERITY: Record<GroupDotState, number> = { ok: 1, exempt: 2, observe: 3, back: 4, stopped: 5 }

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

const HAN = /\p{Script=Han}/u
const ALNUM = /[A-Za-z0-9]/

/**
 * A group's pill text: its alias, or when it has none, the first two Chinese characters of its name, or else its
 * first three letters and digits ("Gemini Ultra" → "Gem").
 */
export function groupPillText(group: { name: string; alias?: string | null }): string {
  const alias = group.alias?.trim()
  if (alias) return alias
  const chars = Array.from(group.name ?? '')
  const han = chars.filter((char) => HAN.test(char))
  if (han.length) return han.slice(0, 2).join('')
  const alnum = chars.filter((char) => ALNUM.test(char))
  if (alnum.length) return alnum.slice(0, 3).join('')
  return chars.slice(0, 2).join('')
}

/** Text made of ASCII only is set at 11px so three letters fit the pill. */
export const isLatinPillText = (text: string) => text.length > 0 && Array.from(text).every((char) => char.charCodeAt(0) < 0x80)
