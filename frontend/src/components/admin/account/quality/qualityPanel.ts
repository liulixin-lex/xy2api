/**
 * What the quality panel shows for one account (spec 5, prototype), read from the backend's own detail
 * (GET /admin/sched-quality/accounts/:id). Pure functions: no timers, no requests.
 *
 * A group's state for the selected model, first match wins (the same order as the pills' dots, spec 4.3):
 * stopped (the whole account takes no part) → exempt (the group's or the all-groups exemption) → back (the model is
 * substandard in the group while quality scheduling is on, fresh and the group's policy enabled) → observe (the account is still observed, or the group has no row for this model yet) →
 * ok. Each dimension's mark is the backend's verdict for that group and model; nothing is judged here.
 */
import type { BackendAccountDetail, BackendDetailGroup, BackendDim, BackendGroupModel } from '@/api/admin/schedQualityTypes'
import type { GroupDotState } from '@/components/admin/account/table/accountGroups'

export type PanelMark = 'pass' | 'fail' | 'short'

/** The model tabs: every model a group has a row for, in first-seen order (groups by id, models by name). */
export function panelModels(detail: Pick<BackendAccountDetail, 'groups'>): string[] {
  return [...new Set(detail.groups.flatMap((group) => group.by_model.map((row) => row.model)))]
}

export const modelRow = (group: BackendDetailGroup, model: string | null): BackendGroupModel | undefined =>
  model === null ? undefined : group.by_model.find((row) => row.model === model)

/**
 * The backend moves an account to the back only while quality scheduling is switched on, its view is fresh and the
 * group's policy is enabled; otherwise a failing row is evidence, not a place in the order.
 */
const movesToBack = (detail: Partial<Pick<BackendAccountDetail, 'global_enabled' | 'stale'>>, group: BackendDetailGroup) =>
  detail.global_enabled !== false && detail.stale !== true && group.policy.enabled !== false

export function groupModelState(
  detail: Pick<BackendAccountDetail, 'stop' | 'status'> & Partial<Pick<BackendAccountDetail, 'global_enabled' | 'stale'>>,
  group: BackendDetailGroup,
  model: string | null
): GroupDotState {
  if (detail.stop) return 'stopped'
  if (group.exemption) return 'exempt'
  const row = modelRow(group, model)
  if (row?.failing && movesToBack(detail, group)) return 'back'
  if (detail.status === 'observing' || !row) return 'observe'
  return 'ok'
}

/** The backend's verdict of one dimension; a group without a row for the model has no samples. */
export function dimMark(row: BackendGroupModel | undefined, dim: BackendDim): PanelMark {
  const verdict = row?.[dim].verdict
  return verdict === 'pass' || verdict === 'fail' ? verdict : 'short'
}

/** Models with a group at the back (the amber mark on their tab). */
export function backModels(detail: Pick<BackendAccountDetail, 'stop' | 'status' | 'groups'> & Partial<Pick<BackendAccountDetail, 'global_enabled' | 'stale'>>): Set<string> {
  const out = new Set<string>()
  for (const model of panelModels(detail)) {
    if (detail.groups.some((group) => groupModelState(detail, group, model) === 'back')) out.add(model)
  }
  return out
}

/**
 * The tab to show: the requested model when the detail has it (opened from a dot: the group's substandard model),
 * else the tab shown before when it still exists, else the first one.
 */
export function pickModelTab(models: string[], requested: string | null, current: string | null): string | null {
  if (requested && models.includes(requested)) return requested
  if (current && models.includes(current)) return current
  return models[0] ?? null
}

/** Whether 恢复 has something to clear: a substandard row, or a clearable automatic state other than the shared proxy. */
export function panelRecoverable(detail: Pick<BackendAccountDetail, 'groups' | 'auto_states'>): boolean {
  return detail.groups.some((group) => group.by_model.some((row) => row.failing)) ||
    detail.auto_states.some((item) => item.clearable && item.kind !== 'proxy_circuit')
}

/** The exemption covering every group, if any. */
export const allGroupsExemption = (detail: Pick<BackendAccountDetail, 'exemptions'>) =>
  detail.exemptions.find((exemption) => exemption.group_id === 0) ?? null

/**
 * Whether a pointerdown on `target` closes the panel: anywhere outside it, except another element that opens it
 * (another dot switches the content) and layers opened from the panel (the exemption popover, its menus, a confirm
 * dialog). The click itself is never stopped.
 */
export function closesPanel(target: Element | null, panel: Element | null, insideAbove: (el: Element) => boolean): boolean {
  if (!target || !target.isConnected) return false
  if (panel?.contains(target)) return false
  if (target.closest('[data-quality-opener]')) return false
  return !insideAbove(target)
}
