// Admin API types for /admin/sched-quality (sched-v2 contract §6). Field names match the JSON exactly.

export type SchedVerdict = 'pass' | 'marginal' | 'fail' | 'nodata'
export type SchedVerdictFrom = 'real'
export type SchedState = 'normal' | 'cooling'
export type SchedStanding = 'front' | 'back'
export type SchedOverrideMode = 'exempt'
/** Cooling: speed | error | cache | acute_timeout; normal: '' | unverified. */
export type SchedReason =
  | 'speed'
  | 'error'
  | 'cache'
  | 'acute_timeout'
  | 'unverified'
  | ''
/** -1 off, 0 immediately, otherwise idle seconds. */
export type SchedTakeoverIdleSeconds = -1 | 0 | 300 | 900 | 1800

export interface SchedOverride {
  mode: SchedOverrideMode
  all_groups: boolean
  expires_at: string | null
  reason: string
}

// ---- POST /accounts/summary (contract §4.11, §6.1) ----

export type SchedDim = 'ttfo' | 'error' | 'cache'
/** Account-level state word: 正常 / 观察中 / 停用. 「不达标」 only exists per group and model. */
export type SchedAccountState = 'normal' | 'observing' | 'disabled'

/** The stop shown in the status column: one word for manual and automatic, the source told apart by colour. */
export interface SchedStop {
  source: 'manual' | 'auto'
  /** Fixed reason code; the UI maps it to a short word. */
  reason: string
  until: string | null
}

/** red: this dimension makes the account substandard in some group (exempt included); short: n below the minimum. */
export interface SchedMark {
  n: number
  short: boolean
  red: boolean
}

export interface SchedSubstandard {
  group_id: number
  model: string
  dims: SchedDim[]
  until: string
  /** The renewal is based on requests in other groups. */
  other_group: boolean
  exempt: boolean
}

/** group_id 0 = every group. */
export interface SchedExemption {
  group_id: number
  expires_at: string | null
  reason: string
}

export interface SchedAccountSummary {
  account_id: number
  status: SchedAccountState
  /** Latest manual recovery. */
  reset_at: string | null
  stop: SchedStop | null
  /** Every group and model of the account, the last 60 minutes (from the latest recovery at most). */
  ttfo: SchedMark & { p50_ms: number | null }
  error: SchedMark & { rate: number | null }
  cache: SchedMark & { ratio: number | null }
  substandard: SchedSubstandard[]
  exemptions: SchedExemption[]
}

export interface SchedAccountsSummary {
  at: string
  stale: boolean
  global_enabled: boolean
  accounts: SchedAccountSummary[]
}

// ---- GET /accounts/:id (contract §4.11) ----

export interface SchedGroupThresholds {
  ttft_seconds: number
  max_error_rate: number
  min_cache_ratio: number
  takeover_idle_seconds: SchedTakeoverIdleSeconds
}

export interface SchedPolicy extends SchedGroupThresholds {
  enabled: boolean
}

/** One dimension of one group and model: the evidence the verdict used (contract §4.3) and the verdict. */
export interface SchedDimDetail {
  /** Where the evidence came from: this group, or every group merged when this group had too few. */
  scope: 'own' | 'merged'
  window_min: number
  n: number
  lcb: number | null
  verdict: 'pass' | 'fail' | 'short'
}

export interface SchedGroupModelDetail {
  model: string
  /** The internal state; an exemption does not change it. */
  failing: boolean
  dims: SchedDim[]
  /** Ladder level 0–5. */
  level: number
  since: string | null
  until: string | null
  /** Groups whose requests renewed this round (到点例外). */
  other_group_ids: number[]
  exempt: boolean
  /** bad = requests over the line (timeouts included). */
  ttfo: SchedDimDetail & { bad: number }
  error: SchedDimDetail & { bad: number }
  cache: SchedDimDetail & { ratio: number | null }
}

export interface SchedAccountGroupDetail {
  group_id: number
  name: string
  policy: SchedPolicy
  /** The exemption in effect for this group (its own or the all-groups one). */
  exemption: SchedExemption | null
  by_model: SchedGroupModelDetail[]
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

export interface SchedAccountDetail {
  account: { id: number; name: string; priority: number; rate: number; concurrency: number; in_flight: number }
  at: string
  stale: boolean
  global_enabled: boolean
  status: SchedAccountState
  stop: SchedStop | null
  reset_at: string | null
  groups: SchedAccountGroupDetail[]
  auto_states: SchedAutoState[]
  exemptions: SchedExemption[]
}

// ---- POST /accounts/:id/reset (contract §6.5) ----

/** `kind` values are fixed codes; the UI maps them to short words. */
export interface SchedResetResult {
  account_id: number
  reset_at: string | null
  cleared: string[]
  kept: { kind: string; until: string | null }[]
  failed: { kind: string }[]
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

// ---- DELETE /accounts/:id/auto-states/:key ----

export interface SchedClearAutoState {
  cleared: boolean
}

// ---- GET | PUT /groups/:id/policy ----

export interface SchedGroupPolicy {
  group_id: number
  version: number
  policy: SchedPolicy
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
