/**
 * The quality summary as backend 0.6.5 sends it (backend/internal/service/sched_admin_dto.go): POST
 * /admin/sched-quality/accounts/summary. The account list reads it as is.
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
