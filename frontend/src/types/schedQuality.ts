// Admin API types for /admin/sched-quality (contract section 13). Field names match the JSON exactly;
// the backend DTOs live in backend/internal/service/sched_admin_dto.go.

export type SchedVerdict = 'pass' | 'marginal' | 'fail' | 'nodata'
export type SchedVerdictFrom = 'real' | 'test'
export type SchedState = 'normal' | 'cooling'
export type SchedStanding = 'front' | 'back'
export type SchedOverrideMode = 'exempt' | 'demote'
export type SchedRequestClass = 'light' | 'regular' | 'heavy' | 'xheavy'
export type SchedTestTier = 'small' | 'medium' | 'large'
export type SchedOutcome = 'ok' | 'timeout' | 'account_fault' | 'other'
/** Cooling: speed | error | cache | acute_timeout | test_*; normal: '' | unverified. */
export type SchedReason =
  | 'speed'
  | 'error'
  | 'cache'
  | 'acute_timeout'
  | 'test_speed'
  | 'test_error'
  | 'test_cache'
  | 'unverified'
  | ''
/** error: the model was not accepted because of an internal error (contract clarification K28). */
export type SchedTestTicketWhy = '' | 'already_running' | 'too_soon' | 'not_schedulable' | 'error'
/** -1 off, 0 immediately, otherwise idle seconds. */
export type SchedTakeoverIdleSeconds = -1 | 0 | 300 | 900 | 1800

export interface SchedOverride {
  mode: SchedOverrideMode
  all_groups: boolean
  expires_at: string | null
  reason: string
}

// ---- POST /accounts/summary ----

export interface SchedAccountSummaryCooling {
  group_id: number
  model: string
  reason: SchedReason
  until: string | null
  waiting_test: boolean
  testing: boolean
}

/** A current manual override; group_id 0 covers every group (contract delta D1). */
export interface SchedAccountSummaryOverride {
  group_id: number
  mode: SchedOverrideMode
  expires_at: string | null
}

export interface SchedAccountSummary {
  account_id: number
  model: string | null
  samples: number
  /** ttfo, error and cache are kept by the API but no longer shown in the list (contract delta C5). */
  ttfo: { p90_ms: number | null; timeout: boolean; n: number }
  error: { rate: number | null; n: number }
  cache: { ratio: number | null; n: number }
  groups: { total: number; normal: number; cooling: number; nodata: number }
  /** One item per cooling group, all of them. */
  cooling: SchedAccountSummaryCooling[]
  /** Always 0. */
  cooling_more: number
  /** Group 0 first, then by group id. */
  overrides: SchedAccountSummaryOverride[]
}

export interface SchedAccountsSummary {
  at: string
  stale: boolean
  global_enabled: boolean
  accounts: SchedAccountSummary[]
}

export interface SchedAccountsSummaryRequest {
  account_ids: number[]
}

// ---- POST /admin/accounts/recent-stats/batch (contract delta C4) ----

/** Set by the backend from its own thresholds; null below the sample minimum. */
export type RecentStatsTier = 'good' | 'mid' | 'bad' | null

export interface AccountRecentStats {
  account_id: number
  ttft: { p50_ms: number | null; n: number; tier: RecentStatsTier }
  /** null while ops monitoring is off. */
  error: { rate: number | null; bad: number; n: number; tier: RecentStatsTier } | null
  cache: { ratio: number | null; n: number; tier: RecentStatsTier }
}

export interface AccountRecentStatsBatch {
  as_of: string
  /** One item per requested account, by account id. */
  items: AccountRecentStats[]
}

// ---- GET /accounts/attention ----

export interface SchedAttention {
  cooling: number[]
}

// ---- GET /accounts/:id ----

export interface SchedClassStat {
  class: SchedRequestClass
  n: number
  p90_ms: number | null
}

export interface SchedTestTierResult {
  tier: SchedTestTier
  /** Warm turns actually sent, in order, at most two; null for a turn without first output (see timeouts). */
  ttfo_ms: (number | null)[]
  cache_ratio: number | null
  faults: number
  timeouts: number
}

export interface SchedTest {
  id: string
  at: string
  running: boolean
  done: boolean
  abort_reason: string
  auto: boolean
  tiers: SchedTestTierResult[]
  input_tokens: number
  cached_tokens: number
  output_tokens: number
}

export interface SchedModelEvidence {
  /** Evidence key. */
  model: string
  /** Request model that maps to this evidence key for the account; "" when none (contract clarification K27). */
  request_model: string
  samples: number
  sessions: number
  from: string | null
  to: string | null
  ttfo: {
    n: number
    sessions: number
    timeouts: number
    p90_ms: number | null
    p90_timeout: boolean
    fail_bound_ms: number | null
    fail_bound_timeout: boolean
    by_class: SchedClassStat[]
  }
  error: { n: number; sessions: number; bad: number; rate: number | null; lcb: number | null }
  cache: { turns: number; sessions: number; ratio: number | null }
  test: SchedTest | null
}

export interface SchedGroupThresholds {
  ttft_seconds: number
  max_error_rate: number
  min_cache_ratio: number
  takeover_idle_seconds: SchedTakeoverIdleSeconds
}

export interface SchedGroupModelRow {
  model: string
  state: SchedState
  level: number
  reason: SchedReason
  since: string | null
  until: string | null
  waiting_test: boolean
  testing: boolean
  ttfo: { verdict: SchedVerdict; from: SchedVerdictFrom | null; n: number; p90_ms: number | null; p90_timeout: boolean; limit_ms: number }
  error: { verdict: SchedVerdict; from: SchedVerdictFrom | null; n: number; rate: number | null; limit: number }
  cache: { verdict: SchedVerdict; from: SchedVerdictFrom | null; n: number; ratio: number | null; limit: number }
}

export interface SchedAccountGroup {
  group_id: number
  name: string
  gate_enabled: boolean
  policy: SchedGroupThresholds
  override: SchedOverride | null
  by_model: SchedGroupModelRow[]
}

export interface SchedAutoState {
  key: string
  kind: string
  scope: 'account' | 'model' | 'proxy'
  model: string
  until: string | null
  /** [current, threshold] for counters that have not tripped yet. */
  count: [number, number] | null
  /** Other accounts affected when scope is proxy. */
  shared: number
  clearable: boolean
  why: string
}

export interface SchedRecentAttempt {
  at: string
  group_id: number
  model: string
  class: SchedRequestClass
  stream: boolean
  warm: boolean
  ttfo_ms: number | null
  outcome: SchedOutcome
  detail: string
}

export interface SchedAccountDetail {
  account: { id: number; name: string; priority: number; rate: number; concurrency: number; in_flight: number }
  at: string
  stale: boolean
  global_enabled: boolean
  tests_enabled: boolean
  reset_at: string | null
  models: SchedModelEvidence[]
  groups: SchedAccountGroup[]
  auto_states: SchedAutoState[]
  recent_attempts: SchedRecentAttempt[]
}

// ---- POST /accounts/:id/reset (also POST /admin/accounts/:id/recover-state) ----

export interface SchedResetResult {
  reset_at: string | null
  cleared: { key: string; kind: string }[]
  preserved: { key: string; kind: string; why: string }[]
  failed: { key: string; kind: string; error: string }[]
}

// ---- PUT /accounts/:id/override ----

export interface SchedOverrideRequest {
  /** 0 = every group of the account. */
  group_id: number
  mode: SchedOverrideMode
  /** null = no expiry. */
  expires_at: string | null
  reason: string
}

// ---- POST /accounts/:id/test ----

export interface SchedTestTicket {
  model: string
  test_id: string
  started: boolean
  why: SchedTestTicketWhy
}

export interface SchedTestStart {
  tests: SchedTestTicket[]
}

// ---- DELETE /accounts/:id/auto-states/:key ----

export interface SchedClearAutoState {
  cleared: boolean
}

// ---- GET | PUT /groups/:id/policy ----

export interface SchedPolicy extends SchedGroupThresholds {
  enabled: boolean
}

export type SchedClassLimits = Record<SchedRequestClass, number>

export interface SchedGroupPolicy {
  group_id: number
  version: number
  policy: SchedPolicy
  /** Per model, plus "default" for every other model. */
  class_limits_ms: Record<string, SchedClassLimits>
}

export interface SchedGroupPolicyUpdate {
  expected_version: number
  policy: SchedPolicy
}

// ---- GET /groups/:id/board?model= ----

export interface SchedEvidenceNumbers {
  ttfo: { n: number; sessions: number; p90_ms: number | null; p90_timeout: boolean; fail_bound_ms: number | null; fail_bound_timeout: boolean }
  error: { n: number; sessions: number; rate: number | null; lcb: number | null }
  cache: { n: number; sessions: number; ratio: number | null }
}

export interface SchedBoardAccount extends SchedEvidenceNumbers {
  account_id: number
  name: string
  /** Evidence key behind this row's numbers and state (contract clarification K27). */
  evidence_model: string
  rank: number
  eligible: boolean
  reject: string
  reject_until: string | null
  standing: SchedStanding
  state: SchedState
  level: number
  until: string | null
  waiting_test: boolean
  testing: boolean
  override: SchedOverride | null
  priority: number
  rate: number
  in_flight: number
  concurrency: number
  recently_full_seconds: number
  can_take_over: boolean
  verdicts: { ttfo: SchedVerdict; error: SchedVerdict; cache: SchedVerdict }
}

export interface SchedBoard {
  group_id: number
  /** Request model of this board. */
  model: string
  /** Request models with samples in the group, most samples first. */
  models: string[]
  at: string
  stale: boolean
  global_enabled: boolean
  /** In current selection order; render as given. */
  accounts: SchedBoardAccount[]
}
