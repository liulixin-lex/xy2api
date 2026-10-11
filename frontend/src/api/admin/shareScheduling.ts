/**
 * Admin share scheduling API.
 * Policies are versioned per group; saves use expected_version (409 on conflict).
 */

import { apiClient } from '../client'

export type ShareScheduler = 'sub2api' | 'shadow' | 'share'

export interface ShareSchedulingSettings {
  enabled: boolean
  cache_attribution: boolean
  attribution_sample_rate: number
  decision_samples: boolean
}

export interface ShareAccountRule {
  account_id: number
  tier: number
  weight: number
  drain?: boolean
  drain_until?: string
  cache_domain?: string
}

export interface SharePolicy {
  schema: number
  scheduler: ShareScheduler
  accounts: ShareAccountRule[]
  defaults: { tier: 'account_priority'; weight: 'concurrency' | 'equal' }
  affinity: {
    enabled: boolean
    key: 'auto' | 'session' | 'prefix' | 'none'
    ttl_seconds: number
    wait: { mode: 'auto' | 'fixed' | 'off'; ms_per_1k_tokens: number; max_ms: number; max_waiting: number }
    on_spill: 'rebind' | 'keep'
    soft_spill_max_seconds: number
  }
  cache: { ttl_seconds: number; realign: 'cold' | 'off' }
  tiers: {
    on_saturated: 'spill' | 'wait'
    on_failed: 'next_tier' | 'stop'
    return: 'cold' | 'immediate' | 'session_end'
    stable_seconds: number
    return_deadline_seconds: number
  }
  balance: {
    unit: 'sessions' | 'requests' | 'tokens' | 'standard_cost'
    correction: 'off' | 'gentle' | 'strong'
    window_minutes: number
    min_volume: number
  }
}

export interface ShareMember {
  account_id: number
  name: string
  platform: string
  type: string
  priority: number
  concurrency: number
  status: string
  schedulable: boolean
  healthy: boolean
  reason?: string
  recover_at?: string
  tier: number
  weight: number
  configured: boolean
  drain: boolean
  drain_until?: string
  cache_domain: string
  target_share: number
  effective_share: number
  capacity_share: number
}

export interface ShareGroupSummary {
  group_id: number
  name: string
  platform: string
  scheduler: ShareScheduler
  version: number
  configured: boolean
  effective: boolean
  invalid?: boolean
  imported_from?: string
  updated_at?: string
  requests_1h: number
  cache_read_ratio_1h: number
  scheduling_miss_ratio_1h: number
  shadow_agree_rate_1h?: number
}

export interface SharePolicyView {
  group_id: number
  group_name: string
  platform: string
  version: number
  scheduler: ShareScheduler
  effective: boolean
  policy: SharePolicy
  invalid?: string
  imported_from?: string
  updated_by?: number
  updated_at?: string
  active_tier: number
  members: ShareMember[]
}

export interface ShareWarning {
  code: string
  account_id?: number
  tier?: number
  detail?: string
}

export interface SharePreview {
  active_tier: number
  members: ShareMember[]
  placement_change: number
  sampled_keys: number
  deadline_accounts?: number[]
  estimated_migrations: number
  warnings: ShareWarning[]
}

export interface ShareExplainInput {
  model?: string
  session_hash?: string
  estimated_tokens?: number
  cache_one_hour?: boolean
  body?: unknown
}

export interface ShareExplainEntry {
  account_id: number
  tier: number
  weight: number
  effective_weight: number
  configured: boolean
  drain: boolean
  cache_domain: string
  class?: string
  eligible: boolean
  excluded: boolean
  reason?: string
  rank: number
}

export interface ShareExplanation {
  group_id: number
  platform: string
  scheduler: ShareScheduler
  version: number
  key_source: string
  session?: string
  bound_account_id?: number
  idle_ms: number
  cache_ttl_seconds: number
  estimated_tokens: number
  cacheable: boolean
  warm: boolean
  wait_budget_ms: number
  reason: string
  steps: number[]
  write: string
  spill_write: string
  spill_reason?: string
  max_waiting?: number
  target?: number
  ideal?: number
  active_tier: number
  entries: ShareExplainEntry[]
}

export interface ShareAccountActual {
  account_id: number
  placed: number
  placed_share: number
  requests: number
  request_share: number
  tokens: number
  token_share: number
  standard_cost: number
  cost_share: number
  /** Times the account was tried but passed over, by reason. */
  skipped?: Record<string, number>
  /** Sampled first-token latency of requests the account served. */
  ttft?: ShareTTFT
}

export interface ShareTTFT {
  count: number
  p50_ms: number | null
  p95_ms: number | null
  p99_ms: number | null
}

export interface ShareReport {
  group_id: number
  scheduler: ShareScheduler
  version: number
  unit: SharePolicy['balance']['unit']
  from: string
  to: string
  active_tier: number
  members: ShareMember[]
  actual: ShareAccountActual[]
  decisions: {
    requests: number
    reasons: Record<string, number>
    key_sources: Record<string, number>
    ideal_skips: Record<string, number>
    hot_migrations: number
    hot_reasons: Record<string, number>
    wait_count: number
    wait_hit: number
    avg_wait_ms: number
    attempts: number
    shadow: number
    shadow_agree: number
    shadow_agree_rate: number
    shadow_reasons: Record<string, number>
    /** Shadow comparisons skipped at the concurrency bound. */
    shadow_skipped?: number
    rebalance_cleared: number
    dropped: number
  }
  cache: {
    requests: number
    classes: Record<string, number>
    class_tokens: Record<string, number>
    prompt_tokens: number
    cache_read_tokens: number
    cache_creation_tokens: number
    read_ratio: number
    scheduling_miss_ratio: number
    ttft: Record<string, ShareTTFT>
    /** Sampled selection cost keyed by share, shadow or original. */
    selection?: Record<string, ShareSelectionCost>
    dropped: number
  }
  corrections?: Record<string, number>
  counters_error?: string
}

/** An account's resolved rule in one group that stores a policy. */
export interface ShareAccountGroupRule {
  group_id: number
  group_name: string
  scheduler: ShareScheduler
  effective: boolean
  configured: boolean
  tier: number
  weight: number
  drain: boolean
  drain_until?: string
  cache_domain: string
}

/** Redis and database figures need server.enable_server_timing; they are null without metered samples. */
export interface ShareSelectionCost {
  samples: number
  avg_ms: number
  io_samples: number
  avg_redis_round_trips: number | null
  avg_redis_ms: number | null
  avg_db_queries: number | null
}

export interface ShareDecisionSample {
  at: string
  node?: string
  version: number
  scheduler: ShareScheduler
  platform?: string
  model?: string
  key_source: string
  session?: string
  reason: string
  routing?: string
  bound?: number
  target?: number
  ideal?: number
  served?: number
  original?: number
  agree?: boolean
  ideal_skip?: string
  attempts: number
  wait_ms?: number
  idle_ms?: number
  cache_ttl_seconds?: number
  estimated_tokens?: number
  queued?: boolean
  hot_migration?: boolean
  elapsed_us: number
}

export type ShareRebalanceMode = 'cold' | 'all'

const base = '/admin/share-scheduling'

export async function getSettings(): Promise<ShareSchedulingSettings> {
  const { data } = await apiClient.get<ShareSchedulingSettings>(`${base}/settings`)
  return data
}

export async function updateSettings(settings: Partial<ShareSchedulingSettings>): Promise<ShareSchedulingSettings> {
  const { data } = await apiClient.put<ShareSchedulingSettings>(`${base}/settings`, settings)
  return data
}

export async function listGroups(): Promise<ShareGroupSummary[]> {
  const { data } = await apiClient.get<ShareGroupSummary[]>(`${base}/groups`)
  return data ?? []
}

export async function getPolicy(groupId: number): Promise<SharePolicyView> {
  const { data } = await apiClient.get<SharePolicyView>(`${base}/groups/${groupId}/policy`)
  return data
}

export async function updatePolicy(groupId: number, expectedVersion: number, policy: SharePolicy): Promise<SharePolicyView> {
  const { data } = await apiClient.put<SharePolicyView>(`${base}/groups/${groupId}/policy`, {
    expected_version: expectedVersion,
    policy
  })
  return data
}

export async function preview(groupId: number, policy: SharePolicy): Promise<SharePreview> {
  const { data } = await apiClient.post<SharePreview>(`${base}/groups/${groupId}/preview`, { policy })
  return data
}

export async function explain(groupId: number, input: ShareExplainInput): Promise<ShareExplanation> {
  const { data } = await apiClient.post<ShareExplanation>(`${base}/groups/${groupId}/explain`, input)
  return data
}

export async function report(groupId: number, window = '1h'): Promise<ShareReport> {
  const { data } = await apiClient.get<ShareReport>(`${base}/groups/${groupId}/report`, { params: { window } })
  return data
}

export async function decisions(groupId: number, limit = 100): Promise<ShareDecisionSample[]> {
  const { data } = await apiClient.get<ShareDecisionSample[]>(`${base}/groups/${groupId}/decisions`, { params: { limit } })
  return data ?? []
}

export async function accountRules(accountId: number): Promise<ShareAccountGroupRule[]> {
  const { data } = await apiClient.get<ShareAccountGroupRule[]>(`${base}/accounts/${accountId}/rules`)
  return data ?? []
}

export async function rebalance(groupId: number, mode: ShareRebalanceMode): Promise<{ mode: ShareRebalanceMode; cleared: number }> {
  const { data } = await apiClient.post<{ mode: ShareRebalanceMode; cleared: number }>(`${base}/groups/${groupId}/rebalance`, { mode })
  return data
}

export const shareSchedulingAPI = {
  getSettings,
  updateSettings,
  listGroups,
  getPolicy,
  updatePolicy,
  preview,
  explain,
  report,
  decisions,
  rebalance,
  accountRules
}

export default shareSchedulingAPI
