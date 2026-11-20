export type GroupRowAction = 'board' | 'edit' | 'duplicate' | 'composite' | 'rate' | 'rpm' | 'delete'
export type GroupRowActions = Record<GroupRowAction, boolean>

/**
 * Which actions a group row offers, the same rules for the wide row's buttons and the "⋯" menu: the quality board
 * for OpenAI groups (not in simple mode), the managing actions with the groups permission, user rates also with the
 * balance permission, composite routes for composite groups.
 */
export function groupRowActions(
  group: { platform: string },
  can: { simpleMode: boolean; manage: boolean; adjustBalance: boolean },
): GroupRowActions {
  const full = !can.simpleMode
  return {
    board: full && group.platform === 'openai',
    edit: can.manage,
    duplicate: full && can.manage,
    composite: full && can.manage && group.platform === 'composite',
    rate: full && can.manage && can.adjustBalance,
    rpm: full && can.manage,
    delete: can.manage,
  }
}

export const hasRowActions = (actions: GroupRowActions) => Object.values(actions).some(Boolean)
