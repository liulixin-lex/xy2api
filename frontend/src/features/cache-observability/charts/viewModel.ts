// Pure derivations and formatting for the cache charts. No Vue, no I/O.
import type { Experiment, ExperimentSlice, LossClassStat, TraceStatsResponse } from './types'

const MINUS = '−'
/** A value that is not there (null) shows as an em dash (contract R5). */
export const EMPTY = '—'

export function formatPct(ratio: number | null | undefined, digits = 2): string {
  return typeof ratio === 'number' && Number.isFinite(ratio) ? `${(ratio * 100).toFixed(digits)}%` : EMPTY
}

/** "80.28 ~ 80.32%" for a lower/upper pair; a single value when both round to the same text. */
export function formatInterval(lower: number | null | undefined, upper: number | null | undefined, digits = 2): string {
  const lo = typeof lower === 'number' && Number.isFinite(lower) ? (lower * 100).toFixed(digits) : null
  const hi = typeof upper === 'number' && Number.isFinite(upper) ? (upper * 100).toFixed(digits) : null
  if (lo === null && hi === null) return EMPTY
  if (lo === null || hi === null || lo === hi) return `${hi ?? lo}%`
  return `${lo} ~ ${hi}%`
}

/** Signed percentage-point delta with a real minus sign. */
export function formatPp(delta: number | null | undefined, digits = 2): string {
  if (typeof delta !== 'number' || !Number.isFinite(delta)) return EMPTY
  const text = Math.abs(delta).toFixed(digits)
  if (Number(text) === 0) return `0.${'0'.repeat(digits)}pp`
  return `${delta > 0 ? '+' : MINUS}${text}pp`
}

export function ratioDeltaPp(current: number | null | undefined, previous: number | null | undefined): number | null {
  return typeof current === 'number' && typeof previous === 'number' ? (current - previous) * 100 : null
}

export function formatTokensM(tokens: number | null | undefined): string {
  if (typeof tokens !== 'number' || !Number.isFinite(tokens)) return EMPTY
  return `${(tokens / 1e6).toLocaleString('en-US', { minimumFractionDigits: 1, maximumFractionDigits: 1 })}M`
}

export function formatCount(n: number | null | undefined): string {
  return typeof n === 'number' && Number.isFinite(n) ? n.toLocaleString('en-US') : EMPTY
}

/** Two decimals; `maxDigits` keeps more for small unit prices such as w_m ($0.075, not $0.08). */
export function formatMoney(amount: number | null | undefined, currency: string | null | undefined, maxDigits = 2): string {
  if (typeof amount !== 'number' || !Number.isFinite(amount)) return EMPTY
  const code = currency || 'USD'
  try {
    return new Intl.NumberFormat('en-US', { style: 'currency', currency: code, minimumFractionDigits: 2, maximumFractionDigits: maxDigits }).format(amount)
  } catch {
    return `${amount.toFixed(2)} ${code}`
  }
}

export function formatSignedMoney(delta: number | null | undefined, currency: string | null | undefined): string {
  if (typeof delta !== 'number' || !Number.isFinite(delta)) return EMPTY
  const text = formatMoney(Math.abs(delta), currency)
  return delta === 0 ? text : `${delta > 0 ? '+' : MINUS}${text}`
}

// Judgment order of DESIGN §2.4; P0b splits prefix_changed into _client and _gateway.
export const LOSS_ORDER = [
  'n/a_plugin',
  'n/a_usage',
  'n/a_sampled_out',
  'n/a_truncated',
  'below_min',
  'cold',
  'other_conv',
  'full',
  'prefix_changed',
  'prefix_changed_client',
  'prefix_changed_gateway',
  'provider_reasoning_drop_suspect',
  'under_read',
] as const

export interface SeriesColor {
  light: string
  dark: string
}

// Validated with the dataviz palette script (adjacent order of the waterfall, light #ffffff / dark #1e293b).
export const LOSS_COLORS: Record<string, SeriesColor> = {
  prefix_changed: { light: '#2a78d6', dark: '#3987e5' },
  prefix_changed_client: { light: '#2a78d6', dark: '#3987e5' },
  prefix_changed_gateway: { light: '#e34948', dark: '#e66767' },
  provider_reasoning_drop_suspect: { light: '#1baf7a', dark: '#199e70' },
  under_read: { light: '#eb6834', dark: '#d95926' },
}
export const NEUTRAL_COLOR: SeriesColor = { light: '#9ca3af', dark: '#64748b' }
export const TOTAL_COLOR: SeriesColor = { light: '#374151', dark: '#cbd5e1' }
export const TREND_COLORS = {
  tokenHit: { light: '#0d9488', dark: '#0d9488' },
  requestHit: { light: '#6250d6', dark: '#9085e9' },
}

export function lossOrderIndex(cls: string): number {
  const index = (LOSS_ORDER as readonly string[]).indexOf(cls)
  return index === -1 ? LOSS_ORDER.length : index
}

export type WaterfallKind = 'total' | 'deduct' | 'subtotal' | 'part'

export interface WaterfallStep {
  key: string
  kind: WaterfallKind
  value: number
  share: number
  start: number
  end: number
  color: SeriesColor
}

/**
 * Opportunity cascade (UI_SPEC §4.4): total opportunity, minus the multi-conversation
 * artifact (other_conv), the attributable O′, then O′ split by class in judgment order.
 * Excluded (n/a_*) classes never take part; they are coverage gaps.
 */
export function waterfallSteps(classes: LossClassStat[]): WaterfallStep[] {
  const bearing = classes.filter((c) => !c.excluded && typeof c.opportunity_tokens === 'number' && c.opportunity_tokens > 0)
  const total = bearing.reduce((s, c) => s + (c.opportunity_tokens ?? 0), 0)
  if (total <= 0) return []
  const share = (v: number) => v / total
  const steps: WaterfallStep[] = [{ key: 'total', kind: 'total', value: total, share: 1, start: 0, end: total, color: TOTAL_COLOR }]
  const artifact = bearing.find((c) => c.class === 'other_conv')?.opportunity_tokens ?? 0
  const attributable = total - artifact
  if (artifact > 0) {
    steps.push({ key: 'other_conv', kind: 'deduct', value: artifact, share: share(artifact), start: attributable, end: total, color: NEUTRAL_COLOR })
    steps.push({ key: 'attributable', kind: 'subtotal', value: attributable, share: share(attributable), start: 0, end: attributable, color: TOTAL_COLOR })
  }
  let cursor = attributable
  for (const c of [...bearing].filter((c) => c.class !== 'other_conv').sort((a, b) => lossOrderIndex(a.class) - lossOrderIndex(b.class))) {
    const v = c.opportunity_tokens ?? 0
    steps.push({ key: c.class, kind: 'part', value: v, share: share(v), start: cursor - v, end: cursor, color: LOSS_COLORS[c.class] ?? NEUTRAL_COLOR })
    cursor -= v
  }
  return steps
}

export interface LossRow {
  cls: string
  excluded: boolean
  requests: number
  promptShare: number | null
  tokenHit: number | null
  opportunityShare: number | null
}

export function lossTable(classes: LossClassStat[]): LossRow[] {
  const prompt = classes.reduce((s, c) => s + c.prompt_tokens, 0)
  const opportunity = classes.reduce((s, c) => s + (!c.excluded && typeof c.opportunity_tokens === 'number' ? c.opportunity_tokens : 0), 0)
  return [...classes]
    .sort((a, b) => lossOrderIndex(a.class) - lossOrderIndex(b.class))
    .map((c) => ({
      cls: c.class,
      excluded: c.excluded,
      requests: c.requests,
      promptShare: prompt ? c.prompt_tokens / prompt : null,
      tokenHit: c.prompt_tokens ? c.read_tokens / c.prompt_tokens : null,
      opportunityShare: c.excluded || typeof c.opportunity_tokens !== 'number' || !opportunity ? null : c.opportunity_tokens / opportunity,
    }))
}

export interface CoverageSegments {
  judgeable: number
  excluded: number
  untraced: number
  excludedBreakdown: { cls: string; share: number; requests: number }[]
}

/** Shares of the in-scope business prompt tokens (DESIGN §2.7 acceptance (a)). */
export interface StatsScope {
  /** pck, session headers and the category completeness count OpenAI only (`openai_only_metrics`). */
  openaiOnly: boolean
  /** The OpenAI partition holds Messages-bridge requests only (`openai_native_excluded`). */
  bridgeOnly: boolean
  /** The queried platform is not observed (`platform_not_covered`). */
  notCovered: boolean
  /** A provider view: the OpenAI-only metrics are [] or null there, so the cards hide them. */
  openaiMetricsAbsent: boolean
}

/** What the M2b `scope_notes` mean for the stats cards (cache_panel_contract/m2b README §1.3, §1.5). */
export function statsScope(stats: TraceStatsResponse): StatsScope {
  const notes = stats.scope_notes ?? []
  const openaiOnly = notes.includes('openai_only_metrics')
  return {
    openaiOnly,
    bridgeOnly: notes.includes('openai_native_excluded'),
    notCovered: notes.includes('platform_not_covered'),
    openaiMetricsAbsent:
      openaiOnly &&
      !stats.pck_in.length &&
      !stats.pck_out.length &&
      !stats.session_headers.length &&
      !stats.sess_out_relation.length &&
      stats.pck_out_is_uuid_ratio === null,
  }
}

export function coverageSegments(stats: TraceStatsResponse): CoverageSegments | null {
  const business = stats.coverage.business_prompt_tokens
  if (!business) return null
  const excludedClasses = stats.loss_classes.filter((c) => c.excluded)
  const excludedP = excludedClasses.reduce((s, c) => s + c.prompt_tokens, 0)
  const traced = stats.coverage.traced_prompt_tokens
  return {
    judgeable: Math.max(0, traced - excludedP) / business,
    excluded: excludedP / business,
    untraced: Math.max(0, business - traced) / business,
    excludedBreakdown: excludedClasses
      .sort((a, b) => lossOrderIndex(a.class) - lossOrderIndex(b.class))
      .map((c) => ({ cls: c.class, share: c.prompt_tokens / business, requests: c.requests })),
  }
}

export const STALE_AFTER_MS = 2 * 3_600_000

export function isStale(asOf: string | null | undefined, now: Date): boolean {
  if (!asOf) return false
  const t = Date.parse(asOf)
  return Number.isFinite(t) && now.getTime() - t > STALE_AFTER_MS
}

export function isTruncated(stats: Pick<TraceStatsResponse, 'window' | 'effective_start'>): boolean {
  return Date.parse(stats.effective_start) > Date.parse(stats.window.start)
}

export interface ExperimentSummary {
  experiment: Experiment
  primary: ExperimentSlice | null
  off: Experiment['arms'][number] | null
  on: Experiment['arms'][number] | null
}

export function runningExperiment(experiments: Experiment[]): ExperimentSummary | null {
  const experiment = experiments.find((e) => e.status === 'running')
  if (!experiment) return null
  return {
    experiment,
    primary: experiment.slices.find((s) => s.slice === 's_r') ?? experiment.slices[0] ?? null,
    off: experiment.arms.find((a) => a.arm === 'off') ?? null,
    on: experiment.arms.find((a) => a.arm === 'on') ?? null,
  }
}

/** Index of the bucket that contains `at`, for placing deploy markers on a category axis. */
export function bucketIndex(at: string, bucketStarts: string[]): number {
  const t = Date.parse(at)
  if (!bucketStarts.length || !Number.isFinite(t)) return -1
  let index = -1
  for (let i = 0; i < bucketStarts.length; i++) {
    if (Date.parse(bucketStarts[i]) <= t) index = i
    else break
  }
  return index
}
