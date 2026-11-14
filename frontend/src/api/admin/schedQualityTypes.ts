/**
 * Quality scheduling as backend 0.6.5 sends and takes it (backend/internal/service/sched_admin_dto.go and
 * internal/handler/admin/sched_quality_handler.go). Every reader uses these shapes as they are; there is no
 * translation layer.
 *
 * - POST /admin/sched-quality/accounts/summary: BackendAccountsSummary
 * - GET /admin/sched-quality/accounts/:id: BackendAccountDetail
 * - POST /admin/sched-quality/accounts/:id/reset: BackendResetResult
 * - PUT | DELETE /admin/sched-quality/accounts/:id/override: SchedOverrideRequest (the exemption is the only override)
 * - DELETE /admin/sched-quality/accounts/:id/auto-states/:key: BackendClearAutoState
 * - GET | PUT /admin/sched-quality/groups/:id/policy (+ POST …/preview): BackendGroupPolicy, SchedGroupPolicyUpdate,
 *   BackendPolicyPreview
 * - GET /admin/sched-quality/groups/:id/order: BackendOrder (the group board)
 */

export type BackendDim = 'ttfo' | 'error' | 'cache'

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

export interface BackendSubstandard {
  group_id: number
  model: string
  dims: BackendDim[]
  until: string
  other_group: boolean
  exempt: boolean
}

export interface BackendAccountSummary {
  account_id: number
  /** normal | observing | disabled */
  status: string
  reset_at: string | null
  /** The whole account takes no part in scheduling (manual off, error, rate limit …). */
  stop: { source: string; reason: string; until: string | null } | null
  ttfo: BackendMark & { p50_ms: number | null }
  error: BackendMark & { rate: number | null }
  cache: BackendMark & { ratio: number | null }
  substandard: BackendSubstandard[]
  exemptions: BackendExemption[]
}

export interface BackendAccountsSummary {
  at: string
  stale: boolean
  global_enabled: boolean
  accounts: BackendAccountSummary[]
}

// ---- GET /accounts/:id ----

/** pass | fail | short (too few samples). */
export type BackendVerdict = 'pass' | 'fail' | 'short'

export interface BackendDimDetail {
  /** own | merged: the evidence window is the group's own or merged with the account's other groups. */
  scope: string
  window_min: number
  n: number
  lcb: number | null
  verdict: BackendVerdict | string
}

/** One model of one group: whether it is substandard (moved to the back), and the verdict of each dimension. */
export interface BackendGroupModel {
  model: string
  failing: boolean
  dims: BackendDim[]
  level: number
  since: string | null
  until: string | null
  other_group_ids: number[]
  exempt: boolean
  ttfo: BackendDimDetail & { bad: number }
  error: BackendDimDetail & { bad: number }
  cache: BackendDimDetail & { ratio: number | null }
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
  /** normal | observing | disabled */
  status: string
  stop: BackendAccountSummary['stop']
  reset_at: string | null
  /** The account's OpenAI groups, by group id. */
  groups: BackendDetailGroup[]
  auto_states: BackendAutoState[]
  exemptions: BackendExemption[]
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
}

/** A version conflict is rejected with HTTP 409. */
export interface SchedGroupPolicyUpdate {
  expected_version: number
  policy: BackendPolicy
}

/** What a policy would do to the group's accounts if saved: counts now and after, and the accounts it would turn. */
export interface BackendPolicyPreview {
  now: { normal: number; below: number }
  after: { normal: number; below: number }
  changed: { account_id: number; name: string; model: string; dims: BackendDim[] }[]
}

// ---- GET /groups/:id/order?model= (the group board) ----

export interface BackendOrderRow {
  /** Place in the selection order. */
  rank: number
  account_id: number
  name: string
  /** normal | below (at the back: substandard) | stopped (takes no part) */
  layer: string
  /** Failing dimensions of a row at the back. */
  dims: BackendDim[]
  until: string | null
  exempt: boolean
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
}
