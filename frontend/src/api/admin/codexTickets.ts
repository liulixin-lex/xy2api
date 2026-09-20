import { apiClient } from '../client'

export type CodexTicketPlan = 'pro' | 'team'

export interface CodexAccountTicketWatchdog {
  enabled: boolean
  trigger_count: number
  last_reason?: 'model_mismatch' | 'state_312' | 'iq_degraded'
  last_triggered_at?: string
}

export interface CodexTicketTask {
  id: string
  source: string
  state: 'queued' | 'harvesting' | 'verifying' | 'waiting' | 'succeeded' | 'unchanged' | 'failed' | 'cancelled'
  wait_reason?: string
  retry_at?: string
  proxy_id?: string
  attempts: number
}
export interface CodexAccountTicketStatus {
  quality?: { mode: string; last_result: string; baseline?: number; latest?: { at: string; score: number }; isolated: boolean; completed_questions: number; next_at: string }
  task?: CodexTicketTask
  standby?: { usable: boolean; expires_at: string; last_replay_at: string }
  budget?: { calls: number; model_calls: number; limit: number; model_limit: number; reserved: number; retry_at?: string }
  config_revision?: string
  updated_at?: string
  issued_at?: string
  first_observed_at?: string
  last_replay_at?: string
  last_business_at?: string
  last_business_result?: string
  last_stage?: string
  last_code?: string
  last_reason?: string
  last_http_status?: number
  observed_length?: number
  counters?: Record<string, number>
  models?: string[]
  tickets?: CodexAccountTicketStatus[]
  missing_policy?: 'block' | 'allow_unprotected'
  protection?: 'protected' | 'unprotected' | 'paused' | 'disabled'
  model_verified?: boolean
  iq_status?: string
  iq_retest?: string
  events?: { at: string; reason: string }[]
  enabled: boolean
  global_enabled: boolean
  model: string
  ticket_plan: CodexTicketPlan
  target_length: number
  proxy_configured: boolean
  proxy_display: string
  fixed_proxy_configured: boolean
  state: 'disabled' | 'global_disabled' | 'waiting' | 'harvesting' | 'ready' | 'error'
  remaining_seconds: number
  // These fields are optional so older API responses and test fixtures remain valid.
  ticket_usable?: boolean
  captured_at?: string
  expires_at?: string
  refreshing?: boolean
  retry_after?: string
  last_error: string
  attempts: number
  watchdog: CodexAccountTicketWatchdog
}

export interface CodexAccountTicketSettings {
  models?: string[]
  missing_policy?: 'block' | 'allow_unprotected'
  enabled?: boolean
  expected_revision?: string
  model?: string
  ticket_plan?: CodexTicketPlan
}

export async function getCodexAccountTicket(accountId: number): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.get<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket`)
  return data
}

export async function saveCodexAccountTicket(accountId: number, settings: CodexAccountTicketSettings): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.patch<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket`, settings)
  return data
}

export async function harvestCodexAccountTicket(accountId: number, model?: string, proxyId?: string, requestId?: string): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.post<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket/harvest`, { ...(model ? { model } : {}), ...(proxyId ? { proxy_id: proxyId } : {}), ...(requestId ? { request_id: requestId } : {}) })
  return data
}
