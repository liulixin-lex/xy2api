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
 * - GET /admin/sched-quality/groups/:id/performance: BackendPerformance (the board's 12-hour view, every model)
 * - GET /admin/sched-quality/accounts/:id/performance: BackendPerformance (the account in each of its OpenAI groups)
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
  /** The line with the latest end, whose end this is; absent from an older backend's answer. */
  until_line?: BackendDim | null
  until: string
  /** The previous rules' stay step; not shown (the rules in force have none). */
  level?: number | null
  /** Each line at the rear on its own, with its own review and the evidence it went to the rear on; absent from an older backend's answer. */
  line_details?: BackendRearLine[] | null
  exempt: boolean
  /**
   * The record really puts the account at the back of the group: not exempt, quality scheduling switched on, its
   * view fresh and the group's policy enabled (the computation the backend's quality filter uses).
   */
  effective: boolean
}

/** A model of a group in which the account is on trial: the trial with the latest end, on `until_line`. */
export interface BackendTrial {
  group_id: number
  model: string
  /** Absent from an older backend's answer, as is the start. */
  until_line?: BackendDim | null
  trial_started_at?: string | null
  /** The trial's end at the latest (it may end sooner). */
  until: string
  /** Each line on trial on its own, with its own end and latest assessment; absent from an older backend's answer. */
  line_details?: BackendTrialLine[] | null
}

/** A line at the rear: when it is reviewed, and the decision that put it there this time (enter, renew or trial_fail). */
export interface BackendRearLine {
  line: BackendDim
  until: string
  /** The same shape as a decision's basis, from the same mapping; absent when the state keeps none. */
  basis?: BackendBasis | null
}

/** A line on trial: its end at the latest and its latest assessment. */
export interface BackendTrialLine {
  line: BackendDim
  until: string
  last_eval: BackendLastEval | null
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
  /**
   * The recent status (accounts table, quality panel), the whole account over the recent window by upstream attempt.
   * First token, from finished streamed attempts: those with a useful output plus those that timed out waiting for
   * it, timeouts sorted last. `p50_ms` is null when the median falls on a timeout (more than half timed out) or n = 0.
   */
  first_output: { n: number; timeouts: number; p50_ms: number | null }
  /**
   * First tokens over 10 s: `slow` of `n`, both counting the timeouts that waited the full 10 s (`timeouts`); shorter
   * timeouts are left out. Exactly 10 s is not slow.
   */
  slow_10s: { n: number; slow: number; timeouts: number }
  /** `failed` of `n` requests this account answers for. */
  error: BackendMark & { rate: number | null; failed: number }
  /** Cache hit ratio over continuation turns (what the line judges). */
  cache: { ratio: number | null; turns: number }
  /** The recent window's start when a reset within the last hour shortened it (RFC3339, as reset_at); else null. */
  recent_since: string | null
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
  /** The tolerance and confidence the verdict was taken with; absent on records before rule 4.0. */
  tolerance?: number | null
  confidence?: number | null
  /** Share over the limit of all the group's accounts on the model (the peers' prior); absent from rule 4.0 on. */
  peer_nu?: number
  /** Confidence that the share over the limit is above the tolerance. */
  p_abs: number
  /**
   * The previous rules' comparison with the front peers: confidence that the share is above theirs by the margin, their
   * upper share and the line compared against (u + margin). Null without enough peer evidence and from rule 4.0 on.
   */
  p_rel: number | null
  u: number | null
  z: number | null
  /** Why the verdict is unknown (rule B's unknown reasons). */
  causes: string[]
  /** The window the samples were taken from, as on the cache line; absent on records from before it was kept. */
  window_from?: string | null
  window_to?: string | null
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
  /** The window the outcomes were taken from, as on the cache line; absent on records from before it was kept. */
  window_from?: string | null
  window_to?: string | null
}

export interface BackendCacheEvidence {
  min_ratio: number
  /** Continuation turns, their token-weighted hit ratio, the sessions and the sessions below the minimum. */
  turns: number
  ratio: number
  sessions: number
  low_sessions: number
  /**
   * From rule 4.0, absent on older records (never read as 0): the low sessions with 10 turns or more of their own, the
   * path a failing verdict took (two adjacent, one at 5 points short, one at 20 points short over 10 turns) and the
   * window the turns were taken from (shown as the opened decision's period).
   */
  low_sessions_10?: number | null
  path?: 'pair' | 'single5' | 'deep10' | null
  window_from?: string | null
  window_to?: string | null
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
  /** The previous rules' stay step; not shown (the rules in force have none). */
  level: number
  /** End of the rear stay. */
  until: string | null
  /** Start of the trial; absent from an older backend's answer. */
  trial_started_at?: string | null
  /** End of the trial at the latest (it may end sooner). */
  trial_until: string | null
  verdict: BackendVerdict
  evidence: BackendEvidenceOf[D]
  /** Groups the evidence comes from (the account's quality is judged on the requests of all its groups). */
  source_groups: number[]
  /** The line's latest real assessment; null on rows written before it was kept, or not assessed yet. */
  last_eval?: BackendLastEval | null
  /**
   * At the rear only: the decision that began this stay at the rear (enter, renew or trial_fail), the same shape as a
   * decision's basis, from the same mapping. Absent on other lines and from an older backend.
   */
  basis?: BackendBasis | null
}

/**
 * Why the latest assessment of a line came out as it did: other accounts of the model in the group have too few samples
 * (peer_short), the account itself has too few (own_short), within the line (within), a first sign of falling short
 * awaiting confirmation (pending), on trial without a verdict (trial_open), judged over the line (over, only with an
 * action), anything else (other). A code the copy does not know reads as other.
 */
export type BackendNoActionReason = 'peer_short' | 'own_short' | 'within' | 'pending' | 'trial_open' | 'over' | 'other'

/** The latest run of identical assessments of a line (same kind, action and reason). */
export interface BackendLastEval {
  /** The latest assessment of the run. */
  at: string
  /** It changed the identity or renewed the stay. */
  acted: boolean
  /** The action (a decisions.action code) when acted, else empty. */
  action: string
  reason: BackendNoActionReason | string
  /** The rule's own reasons (evidence.causes codes), all of them. */
  causes: string[]
  /** The evidence that held it up and how much is needed (weighted, so possibly fractional); null when not relevant. */
  have: number | null
  need: number | null
  /** The first assessment of the run. */
  since: string
  count: number
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
  /** The rule version in force; a decision of any other version is of the previous rules. Absent from an older backend. */
  current_rule_version?: string | null
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
  /**
   * Why a trial ended: fail (back to the rear), good (passed after 15 minutes), deadline (back to normal at its end);
   * the previous rules wrote qualified | no_samples | deadline. Absent on other decisions.
   */
  release_reason?: string | null
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
  /** The error limit in force: the group's or 1 %, whichever is larger. */
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
  /**
   * The range each setting was replayed over; outside it there is no data behind the setting. One value when only it
   * was replayed (rule 4.0: the defaults).
   */
  replayed: Record<SchedTTFTRuleField, [number, number]>
}

/**
 * The values a save sends. The backend requires `peer_margin` (400 INVALID_TTFT_RULE without it); the settings form
 * sends it back as loaded, or the answer's default, and leaves it out only when it has neither.
 */
export type SchedTTFTRuleUpdateValues = Omit<SchedTTFTRuleValues, 'peer_margin'> & { peer_margin?: number }

/** A version conflict is rejected with HTTP 409, an invalid value with 400 INVALID_TTFT_RULE. */
export type SchedTTFTRuleUpdate = SchedTTFTRuleUpdateValues & {
  expected_version: number
}

/** none | relax (any value up) | tighten (any value down) | mixed (counts as relax). */
export type SchedRuleChange = 'none' | 'relax' | 'tighten' | 'mixed'

export interface BackendTTFTRuleSaved extends BackendTTFTRule {
  change: SchedRuleChange
  /** First-output line states cleared and re-judged. */
  cleared: number
}

// ---- GET /groups/:id/performance, GET /accounts/:id/performance ----

/** Why a session on the account was moved to another account. */
export type BackendSwitchReason = 'handoff' | 'takeover' | 'migrate' | 'profit_floor' | 'other'

/** The current settings of a group that has rows. */
export interface BackendPerfGroup {
  group_id: number
  name: string
  ttft_seconds: number
  min_cache_ratio: number
  gate_enabled: boolean
}

/**
 * A line of a performance row: its identity now, the end of its rear stay or trial (with the stay step and the trial's
 * start), its latest assessment, and the units its rule counted from this group and from all (absent or null until the
 * backend sends them).
 */
export interface BackendPerfLine extends BackendSampleCounts {
  identity: BackendIdentity
  /** The previous rules' stay step, not shown; with the trial's start, absent from an older backend's answer. */
  level?: number | null
  until: string | null
  trial_started_at?: string | null
  last_eval: BackendLastEval | null
  /** At the rear only, as on the panel's line (BackendLine.basis). */
  basis?: BackendBasis | null
}

/**
 * One group × account × model with traffic in the window: counts and sums only (the shares are worked out in
 * performance.ts). The score is the account × model's over every group with the gate on, the same in each of its rows.
 */
export interface BackendPerfRow {
  group_id: number
  account_id: number
  name: string
  model: string
  /** The account's priority now. */
  priority: number
  /** Still a member of the group (rows of accounts that left it are kept while the window has their traffic). */
  member: boolean
  /** Requests delivered by this account in the group for this model. */
  requests: number
  /** Streaming requests first sent to this account: known ones (n), slow ones by the group's current limit, from how
   *  many sessions; the comparable ones and the slow count other accounts would have had on them (null when none). */
  first: { n: number; slow: number; sessions: number; expected_n: number; expected_slow: number | null; unknown: number }
  /** Turns that continued a session on this same account. */
  cont: { turns: number; sessions: number; read_tokens: number; input_tokens: number }
  /** Attempts on the account: all, cut off at the first-output limit, other failures. */
  attempts: { n: number; timeouts: number; failures: number }
  switches: Record<BackendSwitchReason, number>
  /** value null: no score (too few comparable first sends); excess is the sum of slow over expected on them. */
  score: { value: number | null; n: number; sessions: number; excess: number }
  lines: { [D in BackendDim]: BackendPerfLine }
}

export interface BackendPerformance {
  /** When the 12-hour figures were worked out. */
  at: string
  /** Start of the window really covered (later than at − 12 h while it fills after a restart). */
  window_from: string
  window_hours: number
  complete: boolean
  stale: boolean
  global_enabled: boolean
  groups: BackendPerfGroup[]
  rows: BackendPerfRow[]
}
