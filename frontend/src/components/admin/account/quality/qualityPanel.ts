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
  BackendLastEval,
  BackendLine
} from '@/api/admin/schedQualityTypes'
import { formatShortTime } from '@/components/admin/account/table/accountTime'
import { DASH, SCHED_DIMS } from '@/utils/schedQualityFormat'

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

/** Whether 恢复 has something to clear: a line at the rear or on trial, or a clearable automatic state. */
export function panelRecoverable(detail: Pick<BackendAccountDetail, 'groups' | 'auto_states'>): boolean {
  return detail.groups.some((group) => group.by_model.some((row) => row.placement !== 'front')) ||
    detail.auto_states.some((item) => item.clearable)
}

/** The exemption covering every group, if any. */
export const allGroupsExemption = (detail: Pick<BackendAccountDetail, 'exemptions'>) =>
  detail.exemptions.find((exemption) => exemption.group_id === 0) ?? null

// ---- Evidence ----

const pad2 = (n: number) => String(n).padStart(2, '0')
/** A share as a figure without its sign, one decimal: 0.7623 → "76.2". */
const shareFigure = (ratio: number) => (ratio * 100).toFixed(1)
/** A threshold as set, without its sign, to two decimals at most: 0.86 → "86", 0.0225 → "2.25". */
const limitFigure = (ratio: number, digits = 2) => String(Number((ratio * 100).toFixed(digits)))

/**
 * A share against its line as the two figures shown, on the same side of each other as the numbers they stand for:
 * below stays below, above stays above, equal stays equal. The share has one decimal, rounded as usual; only when that
 * would put it on the line or across it (0.7999 against 0.8 reads 80.0) is it rounded away from the line instead
 * (79.9), and with more decimals if that is still not enough (the line then shown as finely). Nothing about the verdict
 * is assumed: the side is the numbers' own, the one each line's rule compares (a share equal to its line is on it).
 */
function shareAgainst(share: number, line: number): [string, string] {
  const side = Math.sign(share - line)
  const shown = (digits: number, away: boolean) => {
    const scale = 10 ** digits
    const raw = share * 100 * scale
    const units = !away || side === 0 ? Math.round(raw) : side < 0 ? Math.floor(raw) : Math.ceil(raw)
    return (units / scale).toFixed(digits)
  }
  const tries: [number, boolean][] = [[1, false], [1, true], [2, true], [3, true], [4, true]]
  for (const [digits, away] of tries) {
    const figure = shown(digits, away)
    const bar = limitFigure(line, Math.max(2, digits))
    if (Math.sign(Number(figure) - Number(bar)) === side) return [figure, bar]
  }
  return [String(share * 100), String(line * 100)]
}

/**
 * What a decision read against the line it was judged by — "76.2 / 86" — the one text the panel's cell, the list's tip
 * card and the opened decision all show for it. First output: the share of the samples over the group's limit against
 * the tolerance that decision was taken with (a dash on records from before it was kept: never today's setting);
 * errors: the failure share against the limit; cache: the hit ratio against the minimum. A share with nothing counted
 * is a dash. The two figures are on the same side of each other as the numbers (shareAgainst): a cache reading below its
 * minimum never reads as on it. Null when there is no basis or it has no evidence of its own line.
 */
export function basisReading(basis: BackendBasis | null | undefined): string | null {
  if (!basis) return null
  let pair: [number | null, number | null] | null = null
  if (basis.line === 'ttfo' && basis.ttft) {
    const e = basis.ttft
    pair = [e.n > 0 ? e.k / e.n : null, e.tolerance ?? null]
  } else if (basis.line === 'error' && basis.error) {
    const e = basis.error
    pair = [e.padded > 0 ? e.failed / e.padded : null, e.limit]
  } else if (basis.line === 'cache' && basis.cache) {
    const e = basis.cache
    pair = [e.turns > 0 ? e.ratio : null, e.min_ratio]
  }
  if (!pair) return null
  const [share, line] = pair
  const reading: [string, string] = share !== null && line !== null
    ? shareAgainst(share, line)
    : [share !== null ? shareFigure(share) : DASH, line !== null ? limitFigure(line) : DASH]
  return `${reading[0]} / ${reading[1]}`
}

/** The evidence of a basis's own line, whichever line it is. */
const lineEvidence = (basis: BackendBasis) => (basis.line === 'ttfo' ? basis.ttft : basis.line === 'error' ? basis.error : basis.cache)

/**
 * When the evidence of a decision was taken, "13:05–13:15" in local time: from the later of its cut (where the evidence
 * started over) and its window's start, to its window's end. Only from what the record holds: without its window (older
 * records, and the lines that do not keep it), or with a start at or after its end, null; never a window worked out or
 * its ends swapped here.
 */
export function basisPeriod(basis: BackendBasis | null | undefined): string | null {
  const evidence = basis ? lineEvidence(basis) : undefined
  const from = evidence?.window_from ? Date.parse(evidence.window_from) : Number.NaN
  const to = evidence?.window_to ? Date.parse(evidence.window_to) : Number.NaN
  if (!Number.isFinite(from) || !Number.isFinite(to)) return null
  const cut = basis?.cut ? Date.parse(basis.cut) : Number.NaN
  const start = Number.isFinite(cut) ? Math.max(cut, from) : from
  // A start at or after the end (a cut past the window, an odd record) has no period to show: nothing is swapped.
  if (start >= to) return null
  const clock = (ms: number) => {
    const date = new Date(ms)
    return `${pad2(date.getHours())}:${pad2(date.getMinutes())}`
  }
  return `${clock(start)}–${clock(to)}`
}

/** A figure of a basis: its label (admin.schedQuality.evidence.<label>) and its value. */
export interface BasisFigure {
  label: string
  value: string
}

/**
 * What a decision was taken on: when its evidence was taken, what it read against its line (basisReading), then the
 * units counted (all, and this group's). A figure the record does not have is left out.
 */
export function basisFigures(basis: BackendBasis): { period: string | null; reading: string | null; samples: BasisFigure[] } {
  const samples: BasisFigure[] = []
  if (basis.samples_total != null) samples.push({ label: 'samples', value: String(basis.samples_total) })
  if (basis.samples_local != null) samples.push({ label: 'local', value: String(basis.samples_local) })
  return { period: basisPeriod(basis), reading: basisReading(basis), samples }
}

/** What an assessment's have / need count: weighted samples, sessions, slow sessions, turns or low sessions. */
export type LackUnit = 'samples' | 'sessions' | 'slowSessions' | 'turns' | 'lowSessions'
export const LACK_UNITS: LackUnit[] = ['samples', 'sessions', 'slowSessions', 'turns', 'lowSessions']

/** Every cause each line's assessment can carry (backend assessmentCauses); any other name makes the pair unreadable. */
const LINE_CAUSES: Record<BackendDim, string[]> = {
  ttfo: [
    'no_peers', 'all_peers_rear', 'peer_known_insufficient', 'no_samples', 'mass_insufficient', 'sessions_insufficient',
    'slow_sessions_insufficient', 'absolute_unconfirmed', 'relative_unconfirmed', 'worst_mean_above_tolerance', 'relative_veto'
  ],
  error: ['clear_insufficient', 'bounds_unconfirmed', 'recovery_over_limit'],
  cache: ['turns_insufficient', 'low_sessions_insufficient']
}

/**
 * Which of the assessment's own shortfalls its have / need are, from its causes, picked as the backend picks the pair
 * (backend/internal/domain/schedquality/assessment.go: `classify`, `classifyTTFT`, `consider` — the first "own short"
 * cause it meets keeps the pair; `causes` comes in the order of the table `assessmentCauses`, which is also the order
 * rule B reports them in). First output: samples (`no_samples`, `mass_insufficient`: the weight against 20), else
 * sessions (`sessions_insufficient`, against 3), else slow sessions (`slow_sessions_insufficient`, against 2, which
 * `classifyTTFT` skips while `absolute_unconfirmed` is there too). Cache (`classify`): turns before low sessions of 10
 * turns (0 / 1). Errors: clear outcomes (`clear_insufficient`, against 50), read as samples. Null — no unit word — when
 * there are no causes, a cause the line does not know, or the previous rules' peer causes (their pair was the peers').
 */
export function lackUnit(line: BackendDim, causes: string[]): LackUnit | null {
  if (!causes.length || causes.some((cause) => !LINE_CAUSES[line].includes(cause))) return null
  const has = (cause: string) => causes.includes(cause)
  if (line === 'ttfo') {
    if (has('no_peers') || has('all_peers_rear') || has('peer_known_insufficient')) return null
    if (has('no_samples') || has('mass_insufficient')) return 'samples'
    if (has('sessions_insufficient')) return 'sessions'
    if (has('slow_sessions_insufficient') && !has('absolute_unconfirmed')) return 'slowSessions'
    return null
  }
  if (line === 'cache') return has('turns_insufficient') ? 'turns' : has('low_sessions_insufficient') ? 'lowSessions' : null
  return has('clear_insufficient') ? 'samples' : null
}

/** "会话 2 / 3", or "2 / 3" when the unit cannot be told. */
export const lacksText = (t: Translate, unit: LackUnit | null, have: number, need: number) =>
  t(unit ? `admin.schedQuality.panel.lacksUnit.${unit}` : 'admin.schedQuality.panel.lacks', { have, need })

/**
 * A line's latest assessment on trial as its cell and the list's tip card show it: passing (green), what it still lacks
 * against what it needs with what that is ("会话 2 / 3", whole units: a shortfall never reads as enough; lackUnit), or
 * unknown. A dash before the trial's first assessment (the latest is still the one that opened it) or without any.
 */
export function trialNote(latest: BackendLastEval | null | undefined, line: BackendDim, t: Translate): { text: string; pass: boolean } {
  if (!latest || latest.acted) return { text: DASH, pass: false }
  if (latest.reason === 'within') return { text: t('admin.schedQuality.verdict.pass'), pass: true }
  if (latest.have != null && latest.need != null) {
    return { text: lacksText(t, lackUnit(line, latest.causes ?? []), Math.floor(latest.have), Math.ceil(latest.need)), pass: false }
  }
  return { text: t('admin.schedQuality.verdict.unknown'), pass: false }
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
