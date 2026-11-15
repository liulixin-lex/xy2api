import type { Group, GroupPlatform } from '@/types'

/**
 * Groups an account of `platform` can be placed in: the same platform or a composite group (which takes any concrete
 * platform); an antigravity account with mixed scheduling also takes anthropic and gemini groups. Simple mode hides
 * composite groups. Without a platform every group is eligible. Shared by the group selector and the group picker.
 */
export function eligibleGroupsFor<T extends Pick<Group, 'platform'>>(
  groups: T[],
  platform: GroupPlatform | string | undefined,
  options: { mixedScheduling?: boolean; simpleMode?: boolean } = {}
): T[] {
  let result = options.simpleMode ? groups.filter((group) => group.platform !== 'composite') : groups
  if (!platform) return result
  if (platform === 'antigravity' && options.mixedScheduling) {
    result = result.filter((group) => ['antigravity', 'anthropic', 'gemini', 'composite'].includes(group.platform))
  } else {
    result = result.filter((group) => group.platform === platform || group.platform === 'composite')
  }
  return result
}

interface AccountGroupsLike {
  group_ids?: number[] | null
  groups?: Group[] | null
}

/**
 * Selectable groups for an account: the active groups plus groups the account
 * is still bound to but that are no longer active, so they show ticked and can
 * be unticked. Same rule as the edit-account dialog.
 */
export function mergeSelectableGroups<T extends Group>(active: T[], account: AccountGroupsLike | null | undefined): (T | Group)[] {
  const groups = new Map<number, T | Group>(active.map((group) => [group.id, group]))
  const assignedIds = new Set(account?.group_ids ?? [])
  for (const group of account?.groups ?? []) {
    if (assignedIds.has(group.id) && !groups.has(group.id)) groups.set(group.id, group)
  }
  return Array.from(groups.values())
}

/** Ordered comparison: account groups keep their priority order, so order matters. */
export const sameIdList = (left: readonly number[], right: readonly number[]): boolean =>
  left.length === right.length && left.every((id, index) => id === right[index])
