// Admin API types for /admin/sched-quality (sched-v2 contract §6). Field names match the JSON exactly.

export type SchedOverrideMode = 'exempt'
/** -1 off, 0 immediately, otherwise idle seconds. */
export type SchedTakeoverIdleSeconds = -1 | 0 | 300 | 900 | 1800

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

// ---- POST /groups/summary (contract §6.1, §6.3, §6.4) ----

export interface SchedGroupSummary extends SchedPolicy {
  group_id: number
  /** Each account counted once: stopped, else substandard (after exemptions) in any model, else normal. */
  accounts: { normal: number; below: number; stopped: number }
  /** The last 60 minutes of the group's requests: user-side first output P50, failed share, cache, count. */
  last60: { ttfo_p50_ms: number | null; failed_rate: number | null; cache_ratio: number | null; requests: number }
}

export interface SchedGroupsSummary {
  at: string
  stale: boolean
  groups: SchedGroupSummary[]
}

// ---- POST /groups/:id/policy/preview (contract §4.10) ----

export interface SchedPolicyPreview {
  now: { normal: number; below: number }
  after: { normal: number; below: number }
  /** Accounts that would turn substandard under the new lines. */
  changed: { account_id: number; name: string; model: string; dims: SchedDim[] }[]
}

// ---- GET /groups/:id/order?model= (contract §5.7) ----

export interface SchedOrderRow {
  /** 1-based; hard-unavailable accounts come last with layer `stopped`. */
  rank: number
  account_id: number
  name: string
  layer: 'normal' | 'below' | 'stopped'
  dims: SchedDim[]
  until: string | null
  exempt: boolean
  priority: number
  rate: number
  load_rate: number
  in_flight: number
  concurrency: number
  last_used_at: string | null
}

export interface SchedOrder {
  group_id: number
  /** Request model of this order. */
  model: string
  /** Request models of the group, most requests first. */
  models: string[]
  at: string
  stale: boolean
  global_enabled: boolean
  rows: SchedOrderRow[]
}
