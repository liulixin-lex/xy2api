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
  BackendBasis,
  BackendDecision,
  BackendDetailGroup,
  BackendDim,
  BackendEvidenceOf,
  BackendGroupModel,
  BackendLine
} from '@/api/admin/schedQualityTypes'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { SCHED_DIMS } from '@/utils/schedQualityFormat'

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

const DIMS: BackendDim[] = ['ttfo', 'error', 'cache']

/** An end to compare: a missing or unreadable one comes before every known one. */
export const endTime = (time: string | null | undefined) => {
  const ms = time ? Date.parse(time) : Number.NaN
  return Number.isFinite(ms) ? ms : -Infinity
}

/** The line an answer names, when it is one of the three (an older backend names none). */
export const knownDim = (value: unknown): BackendDim | null => (SCHED_DIMS.includes(value as BackendDim) ? (value as BackendDim) : null)

/**
 * The line a rear or trial place shows: of the lines that hold the group there, the one that ends last (the place
 * lasts till then), and how many hold it; null for the other places.
 */
export function placeLine(row: BackendGroupModel | undefined, place: PanelPlace): { dim: BackendDim; line: BackendLine; held: number } | null {
  if (!row || (place !== 'rear' && place !== 'trial')) return null
  const held = DIMS.filter((dim) => row.lines[dim].identity === place)
  const end = (dim: BackendDim) => endTime(place === 'rear' ? row.lines[dim].until : row.lines[dim].trial_until)
  const dim = held.reduce<BackendDim | null>((last, item) => (last === null || end(item) > end(last) ? item : last), null)
  return dim === null ? null : { dim, line: row.lines[dim], held: held.length }
}

type Translate = (key: string, values?: Record<string, unknown>) => string

/** A rear stay as shown: when it is reviewed. The rules have no steps; the backend's level is not shown. */
export const rearStay = (t: Translate, until: string | null | undefined) =>
  t('admin.schedQuality.stay.rear', { time: formatShortTime(until) })

/** A trial as shown: when it started and when it ends at the latest (it may end sooner). */
export const trialStay = (t: Translate, start: string | null | undefined, end: string | null | undefined) =>
  t('admin.schedQuality.stay.trial', { start: formatShortTime(start), end: formatShortTime(end) })

/** A stay with the line it is of named before it. */
export const lineStay = (t: Translate, line: string, stay: string) => t('admin.schedQuality.stay.line', { line, stay })

// ---- Decisions ----

/** Records of one moment, one action and one model's line, across the groups it was taken in. */
export interface MergedDecision {
  key: string
  at: string
  action: string
  model: string
  line: BackendDim | ''
  until: string | null
  /** Group ids, each once, in record order. */
  groups: number[]
  /** Every record of the entry, each with its own end and evidence. */
  items: BackendDecision[]
}

const minuteOf = (time: string | null) => (time ? Math.floor(Date.parse(time) / 60_000) : '')

/**
 * The decision list as read: records of the same minute, action, model, line, end minute (as shown), rule version and
 * release reason are one entry listing their groups (the engine judges a model's line in every group of the account at
 * once). The entry's title is read from its first record, so records of another rule version (around a switch) or that
 * ended a trial for another reason are another entry. Every record stays in the entry with its own end and evidence.
 * Newest first, as given.
 */
export function mergeDecisions(items: BackendDecision[]): MergedDecision[] {
  const out = new Map<string, MergedDecision>()
  for (const item of items) {
    const key = JSON.stringify([
      minuteOf(item.at), item.action, item.model, item.line, minuteOf(item.until), item.basis?.rule_version ?? '', item.basis?.release_reason ?? ''
    ])
    const entry = out.get(key)
    if (!entry) {
      out.set(key, { key, at: item.at, action: item.action, model: item.model, line: item.line, until: item.until, groups: [item.group_id], items: [item] })
      continue
    }
    entry.items.push(item)
    if (!entry.groups.includes(item.group_id)) entry.groups.push(item.group_id)
  }
  return [...out.values()]
}

/** The mark on each model's tab: rear when a group is at the rear for it, else trial when one is on trial. */
export function modelMarks(detail: PlaceDetail & Pick<BackendAccountDetail, 'groups'>): Map<string, 'rear' | 'trial'> {
  const out = new Map<string, 'rear' | 'trial'>()
  for (const model of panelModels(detail)) {
    const places = detail.groups.map((group) => groupPlace(detail, group, model))
    if (places.includes('rear')) out.set(model, 'rear')
    else if (places.includes('trial')) out.set(model, 'trial')
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

const pct = (ratio: number) => `${(ratio * 100).toFixed(1)}%`
/** A threshold as set: 0.05 → "5%", 0.025 → "2.5%". */
const limitPct = (ratio: number) => `${Number((ratio * 100).toFixed(1))}%`

/** A figure of a basis: its label (admin.schedQuality.evidence.<label>, with `params`) and its value. */
export interface BasisFigure {
  label: string
  params?: Record<string, unknown>
  value: string
}

/**
 * What a decision was taken on, as figures against what they were measured against: the share over the group's limit
 * against the line drawn from the front peers (or the peers' own share when there is no line), the failure share
 * against the limit, the cache hit against the minimum; then the units counted (all, and this group's). A figure the
 * record does not have is left out.
 */
export function basisFigures(basis: BackendBasis): { measure: [BasisFigure, BasisFigure] | null; samples: BasisFigure[] } {
  let measure: [BasisFigure, BasisFigure] | null = null
  if (basis.line === 'ttfo' && basis.ttft && basis.ttft.n > 0) {
    const e = basis.ttft
    measure = [
      { label: 'over', params: { s: e.limit_s }, value: pct(e.k / e.n) },
      e.z != null ? { label: 'bar', value: pct(e.z) } : { label: 'peers', value: pct(e.prior_mu) }
    ]
  } else if (basis.line === 'error' && basis.error && basis.error.padded > 0) {
    const e = basis.error
    measure = [{ label: 'failed', value: pct(e.failed / e.padded) }, { label: 'limit', value: limitPct(e.limit) }]
  } else if (basis.line === 'cache' && basis.cache && basis.cache.turns > 0) {
    const e = basis.cache
    measure = [{ label: 'hit', value: pct(e.ratio) }, { label: 'min', value: limitPct(e.min_ratio) }]
  }
  const samples: BasisFigure[] = []
  if (basis.samples_total != null) samples.push({ label: 'samples', value: String(basis.samples_total) })
  if (basis.samples_local != null) samples.push({ label: 'local', value: String(basis.samples_local) })
  return { measure, samples }
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
