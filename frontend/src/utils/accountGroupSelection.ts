import type { Group } from '@/types'

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
