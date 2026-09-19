import { apiClient } from '../client'

export type CodexTicketPlan = 'pro' | 'team'

export interface CodexAccountTicketWatchdog {
  enabled: boolean
  trigger_count: number
  last_reason?: 'model_mismatch' | 'state_312' | 'iq_degraded'
  last_triggered_at?: string
}

export interface CodexAccountTicketStatus {
  counters?: Record<'rounds_started' | 'rounds_succeeded' | 'rounds_failed' | 'watchdog_triggers', number>
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
  enabled: boolean
  model?: string
  ticket_plan?: CodexTicketPlan
}

export async function getCodexAccountTicket(accountId: number): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.get<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket`)
  return data
}

export async function saveCodexAccountTicket(accountId: number, settings: CodexAccountTicketSettings): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.put<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket`, settings)
  return data
}

export async function harvestCodexAccountTicket(accountId: number, model?: string): Promise<CodexAccountTicketStatus> {
  const { data } = await apiClient.post<CodexAccountTicketStatus>(`/admin/accounts/${accountId}/codex-ticket/harvest`, model ? { model } : {})
  return data
}
