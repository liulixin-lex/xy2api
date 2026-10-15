/**
 * Admin gateway resilience API (design §10, §12).
 * Settings are one JSON document validated by the server; omitted fields keep
 * their current value.
 */

import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface ResilienceRange { min: number; max: number }

export interface GatewayResilienceSettings {
  enabled: boolean
  failure: { transient_cooldown_seconds: number; retry_after_cap_seconds: number; balance_keywords_extra: string[] }
  session: { enabled: boolean; streak_threshold: number; streak_window_seconds: number; avoid_seconds: number }
  last_resort: { enabled: boolean }
  balance: {
    enabled: boolean
    recheck_schedule_seconds: number[]
    jitter_ratio: number
    suspect_ttl_seconds: number
    recovering_concurrency: number
    recovering_window_seconds: number
    recovering_success_target: number
    generation_probe_daily_budget: number
    notify_after_hours: number
  }
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
  }
  fallback_group: { enabled: boolean; return_stable_seconds: number; binding_ttl_seconds: number }
  reservation: { cache_aware_estimate: boolean; output_p99_cap: boolean }
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
  balance_states: Record<string, number>
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

export interface BalanceRecord {
  state: string
  since?: string
  next_check_at?: string
  last_evidence?: string
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

export async function recheckBalance(accountId: number): Promise<BalanceRecord> {
  const { data } = await apiClient.post<BalanceRecord>(`${base}/accounts/${accountId}/balance/recheck`)
  return data
}

export const resilienceAPI = {
  getSettings,
  updateSettings,
  stats,
  listUndelivered,
  recheckBalance
}

export default resilienceAPI
