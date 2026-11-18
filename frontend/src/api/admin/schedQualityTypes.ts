/**
 * Quality scheduling as the backend sends and takes it (quality gate v2: three lines, four identities, three layers;
 * the contract is 实施/01-架构蓝图.md section 4 with 02-裁决与修订.md section D). Every reader uses these shapes as
 * they are; there is no translation layer.
 *
 * - POST /admin/sched-quality/accounts/summary: BackendAccountsSummary
 * - GET /admin/sched-quality/accounts/:id: BackendAccountDetail
 * - GET /admin/sched-quality/accounts/:id/decisions?limit=&before_id=: BackendDecision[]
 * - POST /admin/sched-quality/accounts/:id/reset: BackendResetResult
 * - PUT | DELETE /admin/sched-quality/accounts/:id/override: SchedOverrideRequest (the exemption is the only override)
 * - DELETE /admin/sched-quality/accounts/:id/auto-states/:key: BackendClearAutoState
 * - GET | PUT /admin/sched-quality/groups/:id/policy (+ POST …/preview): BackendGroupPolicy, SchedGroupPolicyUpdate,
 *   BackendPolicyPreview
 * - GET /admin/sched-quality/groups/:id/order?model=: BackendOrder (the group board; any model name, listed or not)
 * - GET | PUT /admin/sched-quality/ttft-rule: BackendTTFTRule, SchedTTFTRuleUpdate
 */

/** The three quality lines: first useful output, errors, cache. */
export type BackendDim = 'ttfo' | 'error' | 'cache'

/**
 * A line's identity for one account × model × group: pass (enough evidence, within the threshold), unknown (not
 * enough evidence; no data is unknown), trial (back from the rear on probation), rear (confirmed below standard).
 */
export type BackendIdentity = 'pass' | 'unknown' | 'trial' | 'rear'

/** Where the three lines put the account in the group: any line at the rear → rear; else any on trial → trial. */
export type BackendPlacement = 'front' | 'trial' | 'rear'

/** The latest assessment of a line, apart from its identity. */
export type BackendVerdict = 'pass' | 'fail' | 'unknown'

export interface BackendMark {
  n: number
  /** Below the sample minimum. */
  short: boolean
  /** This dimension makes the account substandard in some group. */
  red: boolean
}

export interface BackendExemption {
  /** 0 = every group. */
  group_id: number
  expires_at: string | null
  reason: string
}

/** A model of a group at whose rear the account is. */
export interface BackendRear {
  group_id: number
  model: string
  /** The lines at the rear. */
  lines: BackendDim[]
  until: string
  exempt: boolean
  /**
   * The record really puts the account at the back of the group: not exempt, quality scheduling switched on, its
   * view fresh and the group's policy enabled (the computation the backend's quality filter uses).
   */
  effective: boolean
}

/** A model of a group in which the account is on trial; `until` is the trial's end. */
export interface BackendTrial {
  group_id: number
  model: string
  until: string
}

/**
 * A group's identity for the account's dot: any model at the rear (not exempt) → rear; else any on trial → trial;
 * else any line of any model passing → pass; else unknown.
 */
export type BackendGroupStatus = 'rear' | 'trial' | 'pass' | 'unknown'

export interface BackendAccountSummary {
  account_id: number
  /** Each of the account's OpenAI groups by id (as a string key). */
  group_status: Record<string, BackendGroupStatus>
  reset_at: string | null
  /** The whole account takes no part in scheduling (manual off, error, rate limit …). */
  stop: { source: string; reason: string; until: string | null } | null
  /** First token: the first event and the first useful output (what the line judges), and the gap between them. */
  ttfo: { first_event_p50_ms: number | null; first_output_p50_ms: number | null; gap_p50_ms: number | null; n: number }
  error: BackendMark & { rate: number | null }
  /** Cache hit ratio over continuation turns (what the line judges) and over every successful request. */
  cache: { ratio: number | null; turns: number; all_ratio: number | null; all_requests: number }
  rear: BackendRear[]
  trial: BackendTrial[]
  exemptions: BackendExemption[]
}

export interface BackendAccountsSummary {
  at: string
  stale: boolean
  global_enabled: boolean
  accounts: BackendAccountSummary[]
}

// ---- GET /accounts/:id ----

/** Evidence of the first-output line. n, k and x are time-discounted amounts, not request counts. */
export interface BackendTTFOEvidence {
  /** The group's first-output limit, seconds. */
  limit_s: number
  /** Discounted samples, of which k over the limit and x with an unknown outcome. */
  n: number
  k: number
  x: number
  /** The discounted weight of this group's samples (n is every group's); absent or null on records before it was kept. */
  n_local?: number | null
  /** Sessions the samples come from (counted up to 3), and sessions with a request over the limit (up to 2). */
  s: number
  d: number
  /** Share over the limit of the group's other accounts on the model (the prior the estimate shrinks towards). */
  prior_mu: number
  /** Share over the limit of all the group's accounts on the model (the peers' prior). */
  peer_nu: number
  /** Confidence that the share over the limit is above the tolerance. */
  p_abs: number
  /** Confidence that the share is above the peers' by the margin; null without enough peer evidence. */
  p_rel: number | null
  /** Upper estimate of the front peers' share over the limit, and the line compared against (u + margin). */
  u: number | null
  z: number | null
  /** Why the verdict is unknown (rule B's unknown reasons). */
  causes: string[]
}

export interface BackendErrorEvidence {
  limit: number
  /** Requests with a clear outcome, the counted failures among them, and running or unattributed ones. */
  clear: number
  failed: number
  unknown: number
  /** Requests the rate is computed over (missing ones up to 50 count as successes). */
  padded: number
  /** Conservative lower bound of the failure rate, and the same with every unknown counted as a failure. */
  lower_fail: number
  lower_pass: number
  /**
   * A review or a trial would have passed, but the failure share is above the limit, so the verdict is unknown (the
   * account does not recover yet). Omitted when false and on records before it was kept.
   */
  recovery_over_limit?: boolean
}

export interface BackendCacheEvidence {
  min_ratio: number
  /** Continuation turns, their token-weighted hit ratio, the sessions and the sessions below the minimum. */
  turns: number
  ratio: number
  sessions: number
  low_sessions: number
}

export interface BackendEvidenceOf {
  ttfo: BackendTTFOEvidence
  error: BackendErrorEvidence
  cache: BackendCacheEvidence
}

/**
 * The units a line's rule really counted (first output: samples, errors: clear outcomes, cache: turns): those from this
 * group, and all of them, borrowed ones included. Absent or null on records from before they were kept (unknown).
 */
export interface BackendSampleCounts {
  samples_local?: number | null
  samples_total?: number | null
}

/** One line of one group × model. */
export interface BackendLine<D extends BackendDim = BackendDim> extends BackendSampleCounts {
  identity: BackendIdentity
  /** Rear stay step, 0..4 (20, 40, 60, 90, 120 minutes). */
  level: number
  /** End of the rear stay. */
  until: string | null
  /** End of the trial. */
  trial_until: string | null
  verdict: BackendVerdict
  evidence: BackendEvidenceOf[D]
  /** Groups the evidence comes from (the account's quality is judged on the requests of all its groups). */
  source_groups: number[]
}

/** One model of one group: each line's identity and evidence, and the place they add up to. */
export interface BackendGroupModel {
  model: string
  placement: BackendPlacement
  exempt: boolean
  lines: { [D in BackendDim]: BackendLine<D> }
}

/** -1 off, 0 immediately, otherwise idle seconds. */
export type SchedTakeoverIdleSeconds = -1 | 0 | 300 | 900 | 1800

/** A group's thresholds (GET /groups/:id/policy carries the same fields). */
export interface BackendPolicy {
  enabled: boolean
  ttft_seconds: number
  max_error_rate: number
  min_cache_ratio: number
  takeover_idle_seconds: SchedTakeoverIdleSeconds
}

export interface BackendDetailGroup {
  group_id: number
  name: string
  policy: BackendPolicy
  /** The exemption covering this group: its own, else the all-groups one (group_id 0). */
  exemption: BackendExemption | null
  /** Sorted by model; empty for a group without state rows. */
  by_model: BackendGroupModel[]
}

/** An automatic state of the account (rate limit, a model's limit, the IQ gate of a model …). */
export interface BackendAutoState {
  key: string
  kind: string
  /** account | model | proxy */
  scope: string
  model: string
  until: string | null
  /** [current, threshold] for counters that have not tripped yet. */
  count: [number, number] | null
  /** Other accounts affected when scope is proxy. */
  shared: number
  clearable: boolean
  why: string
}

export interface BackendAccountDetail {
  account: { id: number; name: string; priority: number; rate: number; concurrency: number; in_flight: number }
  at: string
  stale: boolean
  global_enabled: boolean
  stop: BackendAccountSummary['stop']
  reset_at: string | null
  /** The account's OpenAI groups, by group id. */
  groups: BackendDetailGroup[]
  auto_states: BackendAutoState[]
  exemptions: BackendExemption[]
}

// ---- GET /accounts/:id/decisions ----

/**
 * Whether a trial that ended without a verdict met full trial seats while picking accounts: full (it did),
 * not_observed (not seen), unknown (could not be read, or a later record overwrote it).
 */
export type BackendTrialSeats = 'full' | 'not_observed' | 'unknown'

/** The evidence a decision was taken on (one of ttft, error, cache, by its line). */
export interface BackendBasis extends BackendSampleCounts {
  rule_version: string
  line: BackendDim
  /** regular | trial | review | relax | clock (the 24-hour clear) */
  kind: string
  /** Omitted when zero. */
  cut?: string | null
  /** The threshold then: first-output limit (seconds), error limit or cache minimum (ratios). */
  threshold: number
  source_groups: number[]
  ttft?: BackendTTFOEvidence
  error?: BackendErrorEvidence
  cache?: BackendCacheEvidence
  /** Only on trial_timeout; null or absent on other decisions and on records before it was kept. */
  trial_seats?: BackendTrialSeats | null
}

/** A change of a line's identity. Records of the previous rules have an empty line and no basis. */
export interface BackendDecision {
  id: number
  at: string
  group_id: number
  model: string
  line: BackendDim | ''
  /** enter | renew | release | trial | trial_pass | trial_fail | trial_timeout | reset | relax | clear */
  action: string
  level: number
  until: string | null
  basis?: BackendBasis | null
}

// ---- POST /accounts/:id/reset (also the account list's "recover state") ----

export interface BackendResetResult {
  account_id: number
  reset_at: string | null
  cleared: string[]
  /** Kinds a reset leaves alone (session avoidance and the like); not failures. */
  kept: { kind: string; until: string | null }[]
  failed: { kind: string }[]
}

// ---- PUT /accounts/:id/override, DELETE /accounts/:id/auto-states/:key ----

export interface SchedOverrideRequest {
  /** 0 = every group of the account. */
  group_id: number
  mode: 'exempt'
  /** null = no end. */
  expires_at: string | null
  reason: string
}

export interface BackendClearAutoState {
  cleared: boolean
}

// ---- GET | PUT /groups/:id/policy, POST /groups/:id/policy/preview ----

export interface BackendGroupPolicy {
  group_id: number
  version: number
  policy: BackendPolicy
  /** The error limit in force: the group's or 5 %, whichever is larger. */
  effective_max_error_rate: number
  /**
   * With the limit in force: the fewest failures among `known` clear outcomes (50, 100) that judge the line bad once.
   * Only examples that can trip are listed: with a high limit one or both are left out, and the list may be empty.
   */
  error_trip_examples: { known: number; failed: number }[]
}

/** The answer to a save: the lines a relaxed threshold cleared and re-judged, and how many line states that was. */
export interface BackendGroupPolicySaved extends BackendGroupPolicy {
  relaxed_lines: BackendDim[]
  cleared: number
}

/** A version conflict is rejected with HTTP 409. */
export interface SchedGroupPolicyUpdate {
  expected_version: number
  policy: BackendPolicy
}

/** Verdicts of one line over the group's account × model keys. */
export interface BackendVerdictCounts {
  pass: number
  unknown: number
  fail: number
}

/**
 * What the thresholds would say on the evidence in the current windows: each line assessed once with the saved and
 * once with the new thresholds. Not a forecast of identities (a tightened line keeps them; a relaxed one is cleared
 * and judged again on saving).
 */
export interface BackendPolicyPreview {
  relaxed_lines: BackendDim[]
  lines: Record<BackendDim, { now: BackendVerdictCounts; after: BackendVerdictCounts }>
  /** At most 50. */
  changed: { account_id: number; name: string; model: string; line: BackendDim; from: BackendVerdict; to: BackendVerdict }[]
}

// ---- POST /groups/summary ----

/** One group over all its models. Fields without data are null. */
export interface BackendGroupSummary {
  group_id: number
  /** Account × model keys by the identity their lines add up to. */
  accounts: { pass: number; unknown: number; trial: number; rear: number }
  last60: {
    /** Users' wait for the first useful output, from entering the gateway and from the end of the request body. */
    e2e_first_output_p50_ms: number | null
    e2e_first_output_p90_ms: number | null
    body_first_output_p50_ms: number | null
    body_first_output_p90_ms: number | null
    requests: number | null
    /** Attempts counted as 400s caused by migrating a session. */
    migration_400: number | null
  }
  /** Rear-type decisions (enter, renew, trial_fail) of the last 24 hours and the distinct account × model keys they hit; null when not read. */
  rear_entries_24h: { decisions: number; account_models: number } | null
}

export interface BackendGroupsSummary {
  at: string
  stale: boolean
  groups: BackendGroupSummary[]
}

// ---- GET /groups/:id/order?model= (the group board) ----

/** front | trial (a free trial seat) | trial_full (every trial seat taken) | rear | stopped (takes no part). */
export type BackendOrderLayer = 'front' | 'trial' | 'trial_full' | 'rear' | 'stopped'

export interface BackendOrderRow {
  /** Place in the selection order. */
  rank: number
  account_id: number
  name: string
  layer: BackendOrderLayer
  /** Each line's identity in this group for the model. */
  lines: Record<BackendDim, BackendIdentity>
  /** End of the rear stay, of the trial or of the stop. */
  until: string | null
  exempt: boolean
  /** Pinned in this group: first in its layer, outside the rate range. */
  pinned: boolean
  /** Why the account is not used at all (profit_floor, profit_threshold …); null or absent when it is. */
  excluded?: string | null
  priority: number
  rate: number
  load_rate: number
  in_flight: number
  concurrency: number
  last_used_at: string | null
}

export interface BackendOrder {
  group_id: number
  /** Request model of this order (the group's busiest without one). */
  model: string
  /** Request models with samples in the group, most samples first. */
  models: string[]
  at: string
  stale: boolean
  global_enabled: boolean
  /** In the current selection order; render as given. */
  rows: BackendOrderRow[]
  /** The model's supply in the group at `at`; null when it could not be read. */
  supply: BackendSupply | null
}

/**
 * Member accounts for the model: declaring it (disabled and unschedulable ones included), of those enabled and
 * schedulable, and of those eligible for it right now on the board's default entry (streaming Responses / HTTP SSE:
 * eligibility, rate limits, cooldowns; free slots are not looked at, and plain text requests may use another entry).
 */
export interface BackendSupply {
  declared: number
  enabled: number
  available: number
  at: string
}

// ---- GET | PUT /ttft-rule (the first-output line's three settings, global) ----

/** Ratios: confidence 0.5 < γ < 1, tolerance 0 < p < 1, margin over the peers 0 ≤ δ < 1. */
export interface SchedTTFTRuleValues {
  confidence: number
  tolerance: number
  peer_margin: number
}

export type SchedTTFTRuleField = keyof SchedTTFTRuleValues

export interface BackendTTFTRule extends SchedTTFTRuleValues {
  version: number
  defaults: SchedTTFTRuleValues
  /** The range each setting was replayed over; outside it there is no data behind the setting. */
  replayed: Record<SchedTTFTRuleField, [number, number]>
}

/** A version conflict is rejected with HTTP 409, an invalid value with 400 INVALID_TTFT_RULE. */
export interface SchedTTFTRuleUpdate extends SchedTTFTRuleValues {
  expected_version: number
}

/** none | relax (any value up) | tighten (any value down) | mixed (counts as relax). */
export type SchedRuleChange = 'none' | 'relax' | 'tighten' | 'mixed'

export interface BackendTTFTRuleSaved extends BackendTTFTRule {
  change: SchedRuleChange
  /** First-output line states cleared and re-judged. */
  cleared: number
}
