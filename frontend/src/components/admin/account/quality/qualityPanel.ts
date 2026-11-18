/**
 * What the quality panel shows for one account, read from the backend's own detail
 * (GET /admin/sched-quality/accounts/:id). Pure functions: no timers, no requests.
 *
 * A group's place for the selected model, first match wins: stopped (the whole account takes no part) → exempt (the
 * group's or the all-groups exemption, or the row's own) → the backend's placement (rear or trial) while quality
 * scheduling is on, fresh and the group's policy enabled → front. Each line shows the backend's identity for that
 * group and model; nothing is judged here. A group without a row for the model has no data: every line is unknown.
 */
import type {
  BackendAccountDetail,
  BackendDetailGroup,
  BackendDim,
  BackendEvidenceOf,
  BackendGroupModel,
  BackendSampleCounts,
  BackendIdentity,
  BackendLine
} from '@/api/admin/schedQualityTypes'
import { secondsUntil } from '@/utils/schedQualityFormat'

export type PanelPlace = 'stopped' | 'exempt' | 'rear' | 'trial' | 'front'

/** The model tabs: every model a group has a row for, in first-seen order (groups by id, models by name). */
export function panelModels(detail: Pick<BackendAccountDetail, 'groups'>): string[] {
  return [...new Set(detail.groups.flatMap((group) => group.by_model.map((row) => row.model)))]
}

export const modelRow = (group: BackendDetailGroup, model: string | null): BackendGroupModel | undefined =>
  model === null ? undefined : group.by_model.find((row) => row.model === model)

/**
 * The backend moves an account to the back only while quality scheduling is switched on, its view is fresh and the
 * group's policy is enabled; otherwise a rear line is evidence, not a place in the order.
 */
const placesAccount = (detail: Partial<Pick<BackendAccountDetail, 'global_enabled' | 'stale'>>, group: BackendDetailGroup) =>
  detail.global_enabled !== false && detail.stale !== true && group.policy.enabled !== false

type PlaceDetail = Pick<BackendAccountDetail, 'stop'> & Partial<Pick<BackendAccountDetail, 'global_enabled' | 'stale'>>

/**
 * What an unknown line in a group is down to besides the evidence. A stale view comes first: before the engine's
 * first cycle (a few seconds after a restart) the backend answers stale with global_enabled false, so only a fresh
 * view tells anything about the switches. A group whose policy is off is not judged at all, whatever the global
 * switch; the global switch off alone only stops layering (judging goes on). null: the gate works and the evidence
 * decides.
 */
export function gateOff(
  detail: Partial<Pick<BackendAccountDetail, 'global_enabled' | 'stale'>>,
  group: BackendDetailGroup
): 'stale' | 'global' | 'group' | null {
  if (detail.stale === true) return 'stale'
  if (group.policy.enabled === false) return 'group'
  return detail.global_enabled === false ? 'global' : null
}

export function groupPlace(detail: PlaceDetail, group: BackendDetailGroup, model: string | null): PanelPlace {
  if (detail.stop) return 'stopped'
  const row = modelRow(group, model)
  if (group.exemption || row?.exempt) return 'exempt'
  if (!row || row.placement === 'front' || !placesAccount(detail, group)) return 'front'
  return row.placement
}

/** A line's identity; a group without a row for the model has no data, which is unknown. */
export const lineIdentity = (row: BackendGroupModel | undefined, dim: BackendDim): BackendIdentity => row?.lines[dim].identity ?? 'unknown'

/** Seconds left of a rear stay or a trial; null for the other identities or without an end. */
export function lineSecondsLeft(line: Pick<BackendLine, 'identity' | 'until' | 'trial_until'> | undefined, now: number): number | null {
  if (line?.identity === 'rear') return secondsUntil(line.until, now)
  if (line?.identity === 'trial') return secondsUntil(line.trial_until, now)
  return null
}

/** Models with a group at the rear (the amber mark on their tab). */
export function backModels(detail: PlaceDetail & Pick<BackendAccountDetail, 'groups'>): Set<string> {
  const out = new Set<string>()
  for (const model of panelModels(detail)) {
    if (detail.groups.some((group) => groupPlace(detail, group, model) === 'rear')) out.add(model)
  }
  return out
}

/**
 * The tab to show: the requested model when the detail has it (opened from a dot: the group's rear model), else the
 * tab shown before when it still exists, else the first one.
 */
export function pickModelTab(models: string[], requested: string | null, current: string | null): string | null {
  if (requested && models.includes(requested)) return requested
  if (current && models.includes(current)) return current
  return models[0] ?? null
}

/** Whether 恢复 has something to clear: a line at the rear or on trial, or a clearable automatic state other than the shared proxy. */
export function panelRecoverable(detail: Pick<BackendAccountDetail, 'groups' | 'auto_states'>): boolean {
  return detail.groups.some((group) => group.by_model.some((row) => row.placement !== 'front')) ||
    detail.auto_states.some((item) => item.clearable && item.kind !== 'proxy_circuit')
}

/** The exemption covering every group, if any. */
export const allGroupsExemption = (detail: Pick<BackendAccountDetail, 'exemptions'>) =>
  detail.exemptions.find((exemption) => exemption.group_id === 0) ?? null

// ---- Evidence ----

/** One evidence field: its label key under admin.schedQuality.evidence.<line>, and its value with the unit. */
export interface EvidenceItem {
  key: string
  value: string
}

const pct = (ratio: number) => `${(ratio * 100).toFixed(1)}%`
/** Discounted amounts are not whole requests: one decimal. */
const mass = (value: number) => value.toFixed(1)
const orDash = (value: number | null, format: (v: number) => string) => (value === null ? '—' : format(value))
/** Rule B counts sessions up to 3 and slow sessions up to 2; at the cap the true count may be higher. */
const capped = (value: number, cap: number) => (value >= cap ? `≥${cap}` : String(value))

/**
 * The evidence of a line (the panel's expanded row and a decision's basis), in reading order. The labels carry the
 * units in the copy (秒, 条, 个); ratios and confidences are percentages here.
 */
export function lineEvidence<D extends BackendDim>(dim: D, evidence: BackendEvidenceOf[D]): EvidenceItem[] {
  if (dim === 'ttfo') {
    const e = evidence as BackendEvidenceOf['ttfo']
    return [
      { key: 'limit_s', value: String(e.limit_s) },
      { key: 'n', value: mass(e.n) },
      { key: 'k', value: mass(e.k) },
      { key: 'x', value: mass(e.x) },
      { key: 's', value: capped(e.s, 3) },
      { key: 'd', value: capped(e.d, 2) },
      { key: 'prior_mu', value: pct(e.prior_mu) },
      { key: 'peer_nu', value: pct(e.peer_nu) },
      { key: 'p_abs', value: pct(e.p_abs) },
      { key: 'p_rel', value: orDash(e.p_rel, pct) },
      { key: 'u', value: orDash(e.u, pct) },
      { key: 'z', value: orDash(e.z, pct) }
    ]
  }
  if (dim === 'error') {
    const e = evidence as BackendEvidenceOf['error']
    return [
      { key: 'limit', value: pct(e.limit) },
      { key: 'clear', value: String(e.clear) },
      { key: 'failed', value: String(e.failed) },
      { key: 'unknown', value: String(e.unknown) },
      { key: 'padded', value: String(e.padded) },
      { key: 'lower_fail', value: pct(e.lower_fail) },
      { key: 'lower_pass', value: pct(e.lower_pass) }
    ]
  }
  const e = evidence as BackendEvidenceOf['cache']
  return [
    { key: 'min_ratio', value: pct(e.min_ratio) },
    { key: 'turns', value: String(e.turns) },
    { key: 'ratio', value: pct(e.ratio) },
    { key: 'sessions', value: String(e.sessions) },
    { key: 'low_sessions', value: String(e.low_sessions) }
  ]
}

/** This group's and all the units of a line, as the copy shows them; null when the record has none (unknown). */
export interface SampleSplit {
  local: string
  total: string
}

/**
 * The units the line's rule really counted (samples, clear outcomes or turns): this group's and all, borrowed ones
 * included; for the first-output line also the discounted weight (this group's n_local of all n). A record from before
 * they were kept has neither: unknown, never worked out from the source groups.
 */
export function lineSamples(
  dim: BackendDim,
  counts: BackendSampleCounts | null | undefined,
  evidence: BackendEvidenceOf[BackendDim] | null | undefined
): { count: SampleSplit | null; weight: SampleSplit | null } {
  const local = counts?.samples_local
  const total = counts?.samples_total
  const count = local == null || total == null ? null : { local: String(local), total: String(total) }
  const ttfo = dim === 'ttfo' ? (evidence as BackendEvidenceOf['ttfo'] | null | undefined) : null
  const weight = ttfo && ttfo.n_local != null ? { local: mass(ttfo.n_local), total: mass(ttfo.n) } : null
  return { count, weight }
}

/** Why a first-output verdict is unknown (rule B's reasons); the other lines carry none. */
export const evidenceCauses = (dim: BackendDim, evidence: BackendEvidenceOf[BackendDim] | undefined): string[] =>
  dim === 'ttfo' ? ((evidence as BackendEvidenceOf['ttfo'] | undefined)?.causes ?? []) : []

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
