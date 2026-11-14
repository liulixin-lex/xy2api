/**
 * Quality scheduling as backend 0.6.5 sends it (backend/internal/service/sched_admin_dto.go). The account list and
 * the quality panel read these shapes as they are.
 *
 * - POST /admin/sched-quality/accounts/summary: BackendAccountsSummary
 * - GET /admin/sched-quality/accounts/:id: BackendAccountDetail
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

/** A group's thresholds (GET /groups/:id/policy carries the same fields). */
export interface BackendPolicy {
  enabled: boolean
  ttft_seconds: number
  max_error_rate: number
  min_cache_ratio: number
  takeover_idle_seconds: number
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
