// Admin cache observability charts: response shapes of the read-only
// /admin/cache/{northstar,breakdown,stats,experiments} endpoints
// (CACHE_PANEL_API §2, with the v1.6 §7.7 revisions and the v1.7 §8 amount
// view, v1.7.4). Ratios are in [0, 1], token counts are integers, money is a
// decimal in `cost.currency`, and `null` means "not computed" (never a disguised zero).

export type SummaryWindow = '24h' | '7d' | '30d'
export type TraceWindow = '1h' | '24h'
export type Granularity = '1h' | '1d'
export type PanelPlatform = 'all' | 'openai' | 'anthropic' | 'gemini' | 'grok'
export type BreakdownDimension = 'group' | 'account' | 'upstream_host' | 'client_family' | 'protocol' | 'model'
export type BreakdownSort = 'opportunity' | 'opportunity_amount' | 'prompt' | 'token_hit'
export type CostView = 'list' | 'charged' | 'upstream'

export interface TimeWindow {
  start: string
  end: string
}

interface PanelEnvelope {
  as_of: string
  generated_at: string
}

export interface NorthStarPrevious {
  token_hit_upper: number
  token_hit_lower: number
  request_hit: number
}

export interface NorthStarSummary {
  requests: number
  prompt_tokens: number
  read_tokens: number
  token_hit_upper: number | null
  token_hit_lower: number | null
  request_hit: number | null
  opportunity_recorded_tokens: number
  business_coverage: number | null
  standardized_hit: number | null
  standardized_weight_coverage: number | null
  previous: NorthStarPrevious | null
  /** v1.7.4 §8.2; null when the settled cost columns are unavailable. */
  cost?: NorthStarCost | null
}

/** `notes` vocabulary of §8.2 (v1.7.4); unknown values are shown as sent. */
export type CostNote =
  | 'cost_weighted_hit_uses_list'
  | 'partial_pricing'
  | 'pricing_changed_in_window'
  | 'upstream_partial_coverage'
  | 'component_mismatch'
  | 'forced_cache_billing_candidates'

/**
 * v1.7.4 §8.2. w_m comes from the settled per-component cost (no pricing
 * table snapshot); amounts are in `currency`, at full precision.
 */
export interface NorthStarCost {
  currency: string
  view: CostView | string
  /** Always `settled_cost` for now. */
  pricing_source: string
  /** A priced model with ≥1% of P saw its implied unit price move by more than 1% within the window. */
  pricing_changed_in_window: boolean
  priced_p_share: number | null
  /** view=upstream only: share of priced P whose upstream multiplier is usable; null otherwise. */
  upstream_covered_p_share: number | null
  priced_models: number | null
  saved_amount: number | null
  opportunity_amount: number | null
  token_hit_cost_weighted_upper: number | null
  token_hit_cost_weighted_lower: number | null
  previous: { saved_amount: number | null; token_hit_cost_weighted_upper: number | null } | null
  notes: (CostNote | string)[]
}

/** v1.7.4 §8.3: the cost header of a breakdown answer. */
export interface BreakdownCost {
  currency: string
  view: CostView | string
  pricing_source: string
  pricing_changed_in_window: boolean
  notes: (CostNote | string)[]
}

export interface NorthStarPoint {
  bucket_start: string
  requests: number
  prompt_tokens: number
  token_hit_upper: number | null
  token_hit_lower: number | null
  request_hit: number | null
  opportunity_recorded_tokens: number
  partial: boolean
  saved_amount?: number | null
  opportunity_amount?: number | null
  token_hit_cost_weighted_upper?: number | null
}

export type PanelEventKind = 'release' | 'restart' | 'config' | 'experiment'

export interface PanelEvent {
  at: string
  kind: PanelEventKind | string
  label: string
  version?: string
  cold_until?: string | null
  /** v1.6 §7.7: set for kind=config events that come from a policy write. */
  policy_key?: string
}

export interface NorthStarScope {
  name: string
  business_prompt_tokens: number
  excluded: Record<string, number>
}

export interface NorthStarResponse extends PanelEnvelope {
  policy_version: string
  window: TimeWindow
  granularity: Granularity
  platform: PanelPlatform
  scope: NorthStarScope
  summary: NorthStarSummary
  series: NorthStarPoint[]
  events: PanelEvent[]
}

export interface BreakdownMetrics {
  requests: number
  prompt_tokens: number
  read_tokens: number
  token_hit_upper: number | null
  token_hit_lower: number | null
  request_hit: number | null
  opportunity_recorded_tokens: number
  opportunity_share: number | null
  saved_amount?: number | null
  opportunity_amount?: number | null
  opportunity_amount_share?: number | null
  token_hit_cost_weighted_upper?: number | null
}

export interface BreakdownRow extends BreakdownMetrics {
  key: string
  label: string
  meta?: Record<string, string | number | boolean | null>
}

export interface BreakdownOther extends BreakdownMetrics {
  count: number
}

export interface BreakdownResponse extends PanelEnvelope {
  policy_version: string
  window: TimeWindow
  dimension: BreakdownDimension
  sort: BreakdownSort
  platform: PanelPlatform
  /** v1.7.4 §8.3; null when the settled cost columns are unavailable. */
  cost?: BreakdownCost | null
  total: {
    requests: number
    prompt_tokens: number
    read_tokens: number
    opportunity_recorded_tokens: number
  }
  rows: BreakdownRow[]
  other: BreakdownOther | null
}

export interface TraceCoverage {
  business_requests: number
  business_prompt_tokens: number
  traced_requests: number
  traced_prompt_tokens: number
  coverage_ratio: number | null
  category_known_ratio: number | null
  targets: {
    coverage_ratio: number
    category_known_ratio: number
  }
}

export interface LossClassStat {
  class: string
  requests: number
  prompt_tokens: number
  read_tokens: number
  opportunity_tokens: number | null
  excluded: boolean
}

export interface PckInStat {
  class: string
  requests: number
  prompt_tokens: number
  read_tokens: number
}

export interface PckOutStat extends PckInStat {
  relay_family: string
  under_read_tokens: number
}

export interface SessionHeaderStat {
  header: string
  in_ratio: number
  out_ratio: number
}

export interface RelationStat {
  relation: string
  requests: number
  prompt_tokens: number
}

export interface RouteReasonStat {
  reason: string
  requests: number
  prompt_tokens: number
  read_tokens: number
}

export interface RebindCauseStat {
  cause: string
  requests: number
}

export interface HostReadEwma {
  host: string
  read_share: number
  samples: number
  half_life_s: number
}

export interface TraceStatsResponse extends PanelEnvelope {
  window: TimeWindow
  effective_start: string
  process_started_at: string
  /** v1.6 §7.7: effective state of cache_observability_policy (kill switch, enabled, diagnostics). */
  trace_enabled: boolean
  trace_policy_rev?: number | null
  schema_version: number
  platforms_covered: string[]
  scope_notes: string[]
  coverage: TraceCoverage
  loss_classes: LossClassStat[]
  pck_in: PckInStat[]
  pck_out: PckOutStat[]
  pck_out_is_uuid_ratio: number | null
  session_headers: SessionHeaderStat[]
  sess_out_relation: RelationStat[]
  route_reasons: RouteReasonStat[]
  rebind_causes: RebindCauseStat[]
  model_isolated_switch_rate: number | null
  host_read_ewma: HostReadEwma[]
}

export type ExperimentStatus = 'planned' | 'running' | 'paused' | 'concluded'
export type ExperimentVerdict = 'insufficient_sample' | 'positive' | 'safe_only' | 'needs_review' | 'stopped'

export interface ExperimentArm {
  arm: 'off' | 'on' | string
  sessions: number
  prompt_tokens: number
  read_tokens: number
  token_hit: number | null
  under_read_share: number | null
  switches_per_100: number | null
  errors: number | null
  attempts: number | null
  undelivered: number | null
}

export interface ExperimentSlice {
  slice: string
  sessions_off: number
  sessions_on: number
  diff_pp: number | null
  ci_low_pp: number | null
  ci_high_pp: number | null
  ci_ready: boolean
}

export interface ExperimentCriterion {
  id: string
  slice: string
  met: boolean | null
}

export interface Experiment {
  id: string
  lever: string
  title: string
  status: ExperimentStatus | string
  started_at: string | null
  ended_at: string | null
  build_id: string
  phase: string
  policy_rev: number
  unit: string
  planned_per_arm: number
  arms: ExperimentArm[]
  slices: ExperimentSlice[]
  criteria: ExperimentCriterion[]
  verdict: ExperimentVerdict | string
}

export interface ExperimentsResponse extends PanelEnvelope {
  experiments: Experiment[]
}

export interface NorthStarQuery {
  window: SummaryWindow
  granularity?: Granularity
  platform: PanelPlatform
  cost_view?: CostView
}

export interface BreakdownQuery {
  dimension: BreakdownDimension
  window: SummaryWindow
  platform: PanelPlatform
  sort: BreakdownSort
  limit?: number
  cost_view?: CostView
}

export interface TraceStatsQuery {
  window: TraceWindow
  platform: PanelPlatform
}

/** The four reads the panel needs; implemented by the live API and by the mock. */
export interface CachePanelClient {
  getNorthStar(query: NorthStarQuery): Promise<NorthStarResponse>
  getBreakdown(query: BreakdownQuery): Promise<BreakdownResponse>
  getTraceStats(query: TraceStatsQuery): Promise<TraceStatsResponse>
  listExperiments(): Promise<ExperimentsResponse>
}
