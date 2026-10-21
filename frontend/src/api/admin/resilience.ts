/**
 * Admin gateway resilience API (design §10, §12).
 * Settings are one JSON document validated by the server; omitted fields keep
 * their current value.
 */

import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'
import type { AccountCapabilityLearned } from '@/utils/accountCapability'

export interface ResilienceRange { min: number; max: number }

export interface GatewayResilienceSettings {
  enabled: boolean
  failure: {
    transient_cooldown_seconds: number
    retry_after_cap_seconds: number
    balance_keywords_extra: string[]
    /** 0.4.4: only real capacity signals count as capacity (W2.1). */
    decision_v2: boolean
  }
  session: { enabled: boolean; streak_threshold: number; streak_window_seconds: number; avoid_seconds: number }
  last_resort: { enabled: boolean }
  balance: { enabled: boolean; pause_seconds: number }
  budget: {
    enabled: boolean
    max_attempts: number
    pre_output_budget_seconds: number
    first_output_timeout_seconds: { default: number; high: number; xhigh: number; max: number }
    anthropic_first_event_timeout_seconds: number
    gemini_first_event_timeout_seconds: number
    model_overrides: Record<string, number>
    same_account_retry_max: number
    gemini_same_account_retries: number
    upstream_retry_after_cap_seconds: number
  }
  keepalive: { enabled: boolean; delay_seconds: number; interval_seconds: number; edges: string[]; skip_official_oauth: boolean }
  stall: { enabled: boolean; text_stall_seconds: number }
  disconnect: {
    enabled: boolean
    cancel_undelivered: boolean
    partial_drain_max_seconds: number
    abuse_guard: { enabled: boolean; threshold_per_hour: number; mode: string }
  }
  errors: {
    enabled: boolean
    no_account_retry_after: ResilienceRange
    config_retry_after_seconds: number
    reservation_retry_after_seconds: number
    negative_cache_seconds: number
    fast_fail: { enabled: boolean; window_seconds: number; threshold: number }
    turn_cap: { enabled: boolean; failures: number; window_seconds: number }
    /** 0.4.4: a synthesized response.failed may carry error.headers.retry-after (W5.3). */
    failed_frame_retry_after: boolean
    /** 0.4.4: Retry-After cap for Codex requests, 0 turns it off (W5.5). */
    codex_retry_after_cap_seconds: number
  }
  fallback_group: { enabled: boolean; return_stable_seconds: number; binding_ttl_seconds: number }
  reservation: { cache_aware_estimate: boolean; output_p99_cap: boolean }
  /** 0.4.4: what may be held back before the first content and how a stream must end (W1.1, W1.2). */
  stream: { prelude_staging: boolean; native_prelude_whitelist: boolean; strict_terminal: boolean }
  /** 0.4.4: learn accounts that only accept streaming requests (W6). */
  capability: { stream_only_learning: boolean; learn_ttl_seconds: number }
}

export interface GatewayResilienceSettingsResponse {
  settings: GatewayResilienceSettings
  defaults: GatewayResilienceSettings
}

export interface ResilienceMetricRow {
  name: string
  group_id?: number
  account_id?: number
  label?: string
  count: number
}

export interface ResilienceStats {
  window_seconds: number
  scope: string
  counters: ResilienceMetricRow[]
}

export interface UndeliveredRequest {
  id: number
  request_id: string
  client_request_id: string
  user_id?: number
  api_key_id?: number
  group_id?: number
  account_id?: number
  platform: string
  model: string
  inbound_endpoint: string
  disconnect_stage: string
  duration_ms: number
  upstream_usage?: Record<string, unknown>
  estimated_upstream_cost: number
  billed: boolean
  created_at: string
}

export interface UndeliveredQuery {
  page?: number
  page_size?: number
  user_id?: number
  api_key_id?: number
  group_id?: number
  since?: string
  until?: string
}

/** One account as gateway resilience sees it (design §4.4). */
export interface ResilienceAccountExplanation {
  account_id: number
  name: string
  platform: string
  priority: number
  tier: 'none' | 'soft' | 'hard'
  reason?: string
  recover_at?: string
  session_failures?: number
  avoided?: boolean
  /** 0.4.4: what was learned about the account, shown whatever the query. */
  capability_learned?: AccountCapabilityLearned[]
}

/** Read-only resilience view of a group and, when given, one session. */
export interface ResilienceExplanation {
  group_id: number
  enabled: boolean
  /** Set when the group rejects the model before any account is chosen. */
  model_restriction?: 'not_allowed' | 'channel_pricing'
  healthy_count: number
  accounts: ResilienceAccountExplanation[]
  last_resort?: { account_id: number; recover_at?: string }
  fallback_group?: { group_id: number; name: string; usable: boolean; fallback_healthy: boolean; would_move: boolean }
  session?: { failures: number; avoided: number[] }
}

export interface ResilienceExplainQuery {
  session_hash?: string
  model?: string
  /** 0.4.4: with stream=false, explain a non-streaming request of this protocol (learned stream-only accounts are skipped). */
  protocol?: 'responses' | 'chat'
  stream?: boolean
}

const base = '/admin'

export async function getSettings(): Promise<GatewayResilienceSettingsResponse> {
  const { data } = await apiClient.get<GatewayResilienceSettingsResponse>(`${base}/settings/gateway-resilience`)
  return data
}

export async function updateSettings(settings: GatewayResilienceSettings): Promise<GatewayResilienceSettingsResponse> {
  const { data } = await apiClient.put<GatewayResilienceSettingsResponse>(`${base}/settings/gateway-resilience`, settings)
  return data
}

export async function stats(window = '1h'): Promise<ResilienceStats> {
  const { data } = await apiClient.get<ResilienceStats>(`${base}/resilience/stats`, { params: { window } })
  return data
}

export async function listUndelivered(query: UndeliveredQuery = {}): Promise<PaginatedResponse<UndeliveredRequest>> {
  const { data } = await apiClient.get<PaginatedResponse<UndeliveredRequest>>(`${base}/resilience/undelivered`, { params: query })
  return data
}

export async function explain(groupId: number, query: ResilienceExplainQuery = {}): Promise<ResilienceExplanation> {
  const { data } = await apiClient.get<ResilienceExplanation>(`${base}/resilience/groups/${groupId}/explain`, { params: query })
  return data
}

export const resilienceAPI = {
  getSettings,
  updateSettings,
  stats,
  listUndelivered,
  explain
}

export default resilienceAPI
