/**
 * The 12-hour performance rows worked out for display (the board's 12-hour view, the panel's "过去 12 小时" and the
 * opened detail all read them from here, so their numbers agree). The backend sends counts and sums only; shares,
 * excess, extra slow, ranks, the red and green marks and the order are worked out here. The minimums only decide
 * what is shown; they change nothing in scheduling.
 */
import type { BackendDim, BackendLastEval, BackendPerfGroup, BackendPerfRow, BackendSwitchReason } from '@/api/admin/schedQualityTypes'
import { fmtDuration } from '@/utils/schedQualityFormat'

/** First sends (and sessions) before a slow share is shown: the score's own minimum. */
export const MIN_FIRST = 20
export const MIN_FIRST_SESSIONS = 2
/** Continued turns (and sessions) before a cache share is shown: the cache line's own minimum. */
export const MIN_TURNS = 20
export const MIN_TURN_SESSIONS = 2
export const MIN_ATTEMPTS = 20
/** Excess, as a share, from which a row is marked slow (red) or fast (green). */
export const FLAG_EXCESS = 0.1
/** One-sided 90% Wilson bound. */
export const WILSON_Z = 1.28

export const SWITCH_REASONS: BackendSwitchReason[] = ['handoff', 'profit_floor', 'takeover', 'migrate', 'other']

export type PerfSortKey = 'requests' | 'slow' | 'excess' | 'extra' | 'cache' | 'timeouts' | 'switches' | 'score'
export type SortDir = 'asc' | 'desc'
export interface PerfSort {
  key: PerfSortKey
  dir: SortDir
}
export const PERF_SORT_KEYS: PerfSortKey[] = ['requests', 'slow', 'excess', 'extra', 'cache', 'timeouts', 'switches', 'score']
/** The first click on a column shows its bad end first: the lowest cache share, the highest of everything else. */
export const firstDir = (key: PerfSortKey): SortDir => (key === 'cache' ? 'asc' : 'desc')
export const DEFAULT_SORT: PerfSort = { key: 'extra', dir: 'desc' }

export interface PerfRank {
  place: number
  of: number
}

/** A performance row with what the views show of it. Null values are "not enough to say". */
export interface PerfView {
  row: BackendPerfRow
  key: string
  group: BackendPerfGroup | undefined
  /** Enough first sends from enough sessions for a slow share. */
  firstEnough: boolean
  slowRate: number | null
  /** What other accounts of the model in the group usually show on the same kind of requests; null without any. */
  expectedRate: number | null
  /** The expected share rests on enough comparable first sends to set it against. */
  expectedEnough: boolean
  /** Slow share minus expected, in tenths of a point (334 is +33.4 points), worked out on the counts. */
  excessTenths: number | null
  /** The same as a share (0.334). */
  excess: number | null
  /** Slow requests above expected (first sends × excess), rounded. */
  extra: number | null
  cacheRate: number | null
  timeoutRate: number | null
  switchTotal: number
  /** Of the group's (or the model's) requests; null when not worked out (the panel). */
  share: number | null
  rank: PerfRank | null
  /** Slow for sure (red), fast for sure (green), or neither. */
  slowMark: 'slow' | 'fast' | null
  cacheLow: boolean
  red: boolean
  /** Every figure short of its minimum: the whole row reads grey. */
  allShort: boolean
}

/**
 * The bounds of a share k / n (Wilson score interval, one-sided with z = WILSON_Z): a slow share is marked only when
 * its lower bound is still above the expected one, so 6 slow of 20 against 20% expected is not.
 */
export function wilson(k: number, n: number, z = WILSON_Z): { lower: number; upper: number } {
  if (n <= 0) return { lower: 0, upper: 1 }
  const p = k / n
  const z2 = z * z
  const denominator = 1 + z2 / n
  const centre = p + z2 / (2 * n)
  const margin = z * Math.sqrt((p * (1 - p)) / n + z2 / (4 * n * n))
  return { lower: Math.max(0, (centre - margin) / denominator), upper: Math.min(1, (centre + margin) / denominator) }
}

export const rowKey = (row: Pick<BackendPerfRow, 'group_id' | 'account_id' | 'model'>) => `${row.group_id}:${row.account_id}:${row.model}`

/** Float noise off (a sum of shares read 2.4999999999999996), then rounded half away from zero. */
const tidy = (value: number) => Math.round(value * 1e9) / 1e9
const roundHalfAway = (value: number) => Math.sign(value) * Math.round(Math.abs(tidy(value)))

/** One row's figures; `share` and `rank` are filled in by perfViews. */
export function perfView(row: BackendPerfRow, group: BackendPerfGroup | undefined): PerfView {
  const { first, cont, attempts } = row
  const firstEnough = first.n >= MIN_FIRST && first.sessions >= MIN_FIRST_SESSIONS
  const slowRate = firstEnough ? first.slow / first.n : null
  const expectedRate = first.expected_slow != null && first.expected_n > 0 ? first.expected_slow / first.expected_n : null
  const expectedEnough = expectedRate !== null && first.expected_n >= MIN_FIRST
  // Slow minus expected on the counts first (the expected count taken to all the known first sends; they are the
  // same when every one is comparable), then points, the mark and the rounding from that, never from a float gap:
  // 30 slow against 20 expected of 100 is +10.0 and marked; 3 against 2.5 of 20 is one extra slow request.
  let excessTenths: number | null = null
  let extra: number | null = null
  if (slowRate !== null && expectedEnough) {
    const over = first.slow * first.expected_n - first.expected_slow! * first.n
    excessTenths = roundHalfAway((over * 1000) / (first.n * first.expected_n))
    extra = roundHalfAway(over / first.expected_n)
  }
  const excess = excessTenths === null ? null : excessTenths / 1000
  const cacheEnough = cont.turns >= MIN_TURNS && cont.sessions >= MIN_TURN_SESSIONS && cont.input_tokens > 0
  const cacheRate = cacheEnough ? cont.read_tokens / cont.input_tokens : null
  const timeoutRate = attempts.n >= MIN_ATTEMPTS ? attempts.timeouts / attempts.n : null
  let slowMark: PerfView['slowMark'] = null
  if (excessTenths !== null) {
    const bounds = wilson(first.slow, first.n)
    const flag = Math.round(FLAG_EXCESS * 1000)
    if (excessTenths >= flag && bounds.lower > expectedRate!) slowMark = 'slow'
    else if (excessTenths <= -flag && bounds.upper < expectedRate!) slowMark = 'fast'
  }
  const cacheLow = cacheRate !== null && group !== undefined && cacheRate < group.min_cache_ratio
  return {
    row,
    key: rowKey(row),
    group,
    firstEnough,
    slowRate,
    expectedRate,
    expectedEnough,
    excessTenths,
    excess,
    extra,
    cacheRate,
    timeoutRate,
    switchTotal: SWITCH_REASONS.reduce((sum, reason) => sum + (row.switches?.[reason] ?? 0), 0),
    share: null,
    rank: null,
    slowMark,
    cacheLow,
    red: slowMark === 'slow' || cacheLow,
    allShort: !firstEnough && cacheRate === null && timeoutRate === null && row.score.value === null
  }
}

/**
 * The views of `rows` (every row of the answer: shares and ranks look past the filters). The share is of the group's
 * requests, or of the model's when `byModel`. The rank is the place by score, lowest first, among the scored rows of
 * the same group, model and priority that are still members; equal scores share a place. An answer that is not the
 * whole group (one account's rows) has neither: `whole: false`.
 */
export function perfViews(rows: BackendPerfRow[], groups: BackendPerfGroup[], options: { byModel: boolean; whole?: boolean }): PerfView[] {
  const groupOf = new Map(groups.map((group) => [group.group_id, group]))
  const totals = new Map<string, number>()
  const base = (row: BackendPerfRow) => (options.byModel ? `${row.group_id}:${row.model}` : `${row.group_id}`)
  for (const row of rows) totals.set(base(row), (totals.get(base(row)) ?? 0) + row.requests)
  const peers = new Map<string, number[]>()
  const peerKey = (row: BackendPerfRow) => `${row.group_id}:${row.model}:${row.priority}`
  for (const row of rows) {
    if (!row.member || row.score.value === null) continue
    const list = peers.get(peerKey(row)) ?? []
    list.push(scoreUnits(row.score.value))
    peers.set(peerKey(row), list)
  }
  return rows.map((row) => {
    const view = perfView(row, groupOf.get(row.group_id))
    if (options.whole === false) return view
    const total = totals.get(base(row)) ?? 0
    view.share = total > 0 ? row.requests / total : null
    const list = row.member && row.score.value !== null ? peers.get(peerKey(row)) : undefined
    if (list) {
      const mine = scoreUnits(row.score.value!)
      view.rank = { place: list.filter((value) => value < mine).length + 1, of: list.length }
    }
    return view
  })
}

/** Scores compared in millionths, as the backend compares them (no float jitter). */
const scoreUnits = (value: number) => Math.round(value * 1_000_000)

function sortValue(view: PerfView, key: PerfSortKey): number | null {
  switch (key) {
    case 'requests':
      return view.row.requests
    case 'slow':
      return view.slowRate
    case 'excess':
      return view.excessTenths
    case 'extra':
      return view.extra
    case 'cache':
      return view.cacheRate
    case 'timeouts':
      return view.timeoutRate
    case 'switches':
      return view.switchTotal
    case 'score':
      return view.row.score.value === null ? null : scoreUnits(view.row.score.value)
  }
}

const byRequests = (a: PerfView, b: PerfView) => b.row.requests - a.row.requests || a.row.account_id - b.row.account_id || a.row.model.localeCompare(b.row.model)

/**
 * Sorted by one column. Rows short of every minimum come last whatever the column and direction; then, within each
 * part, rows short of that column's minimum come after the others, by requests among themselves; ties go by requests
 * (most first), then account id.
 */
export function sortViews(views: PerfView[], sort: PerfSort): PerfView[] {
  const sign = sort.dir === 'asc' ? 1 : -1
  return [...views].sort((a, b) => {
    if (a.allShort !== b.allShort) return a.allShort ? 1 : -1
    const x = sortValue(a, sort.key)
    const y = sortValue(b, sort.key)
    if (x === null || y === null) return x === y ? byRequests(a, b) : x === null ? 1 : -1
    return sign * (x - y) || byRequests(a, b)
  })
}

/** The click on a column: its bad end first, then the other way. */
export const nextSort = (current: PerfSort, key: PerfSortKey): PerfSort =>
  current.key === key ? { key, dir: current.dir === 'asc' ? 'desc' : 'asc' } : { key, dir: firstDir(key) }

/**
 * Why the scheduler has not acted on a red row, for its "now" cell. The line a red mark is about (slow: first output;
 * low cache: cache; first output first when both) and is not at the rear already speaks: its latest assessment's
 * reason when it took no action, nothing when it acted or there is none. Only members of a group with the gate on.
 */
export function noAction(view: PerfView): { dim: BackendDim; eval: BackendLastEval } | null {
  if (!view.red || !view.row.member || view.group?.gate_enabled === false) return null
  const dims: BackendDim[] = [...(view.slowMark === 'slow' ? ['ttfo' as const] : []), ...(view.cacheLow ? ['cache' as const] : [])]
  const line = dims.map((dim) => ({ dim, line: view.row.lines[dim] })).find((item) => item.line && item.line.identity !== 'rear')
  const latest = line?.line.last_eval
  return line && latest && !latest.acted ? { dim: line.dim, eval: latest } : null
}

/** The lines' evidence came entirely from other groups: a line held at the rear with no sample of this group. */
export const evidenceElsewhere = (identity: string, samples: { samples_local?: number | null; samples_total?: number | null } | undefined) =>
  identity === 'rear' && samples?.samples_local === 0 && (samples.samples_total ?? 0) > 0

// ---- Number formats (digits only, the same in both languages) ----

const grouped = new Intl.NumberFormat('en-US')
export const fmtCount = (value: number) => grouped.format(value)
/** 0.531 → "53.1%". */
export const fmtPct = (value: number, digits = 1) => `${(value * 100).toFixed(digits)}%`
/** Points with their sign: 0.335 → "+33.5", −0.128 → "−12.8", 0 → "0.0". */
export function fmtPoints(value: number): string {
  const text = Math.abs(value * 100).toFixed(1)
  if (text === '0.0') return text
  return value > 0 ? `+${text}` : `−${text}`
}
/** Extra slow requests with their sign: 155 → "+155", −658 → "−658". */
export const fmtSigned = (value: number) => (value > 0 ? `+${fmtCount(value)}` : value < 0 ? `−${fmtCount(-value)}` : '0')
/**
 * The score in points, like the excess beside it (the backend's 0.251 is 25.1, its 0.03 margin 3.0), one decimal;
 * only the display is × 100.
 */
export function fmtScore(value: number): string {
  const text = Math.abs(value * 100).toFixed(1)
  return value < 0 && text !== '0.0' ? `−${text}` : text
}
/** An evidence count as recorded (weighted ones may be fractional): 9.25 → "9.3", 20 → "20". */
export const fmtAmount = (value: number) => String(Math.round(value * 10) / 10)

// ---- Why not acted, in words (the board's "now" cell and tips, the detail, the panel's groups) ----

type Translate = (key: string, values?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

/** A model's name, or "模型未知" for the empty one (older switch records the backend could not tie to a model). */
export const modelLabel = (model: string, t: Translate) => model || t('admin.schedQuality.perf.unknownModel')

/** The short reason of an assessment that took no action ("没判：同伴样本不够"); an unknown code reads as other. */
export function noActionShort(latest: BackendLastEval, t: Translate, te: HasKey): string {
  const key = `admin.schedQuality.noAction.short.${latest.reason}`
  return t(te(key) ? key : 'admin.schedQuality.noAction.short.other')
}

/** What held it up against what is needed; "<20" when rounding would make a shortfall read as enough (19.96 of 20). */
export function fmtShortfall(have: number, need: number): { have: string; need: string } {
  const needText = fmtAmount(need)
  const haveText = fmtAmount(have)
  return { have: have < need && haveText === needText ? `<${needText}` : haveText, need: needText }
}

/**
 * The main reason of an assessment, long ("判不了：同组同模型其他号的样本不够（8 / 20），已持续 2 小时 50 分"), whether
 * it acted or not; the counts and the duration only when the backend sent them, the trial's end only when known.
 */
export function reasonLong(latest: BackendLastEval, t: Translate, te: HasKey, options: { trialEnd?: string | null; formatTime?: (at: string) => string } = {}): string {
  const key = `admin.schedQuality.noAction.long.${latest.reason}`
  const known = te(key)
  const counts = latest.have != null && latest.need != null ? t('admin.schedQuality.noAction.counts', fmtShortfall(latest.have, latest.need)) : ''
  let text = known ? t(key, { counts }) : t('admin.schedQuality.noAction.long.other')
  if (latest.reason === 'trial_open' && options.trialEnd && options.formatTime) {
    text += t('admin.schedQuality.noAction.trialEnds', { time: options.formatTime(options.trialEnd) })
  }
  const lasted = Date.parse(latest.at) - Date.parse(latest.since)
  if (Number.isFinite(lasted) && lasted >= 60_000) text += t('admin.schedQuality.noAction.since', { duration: fmtDuration(lasted, t) })
  return text
}
