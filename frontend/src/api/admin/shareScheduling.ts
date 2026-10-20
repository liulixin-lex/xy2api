/**
 * Admin share scheduling API.
 *
 * A share group ranks accounts by their priority and splits new sessions by
 * their load factor; a group has only two settings, the scheduler and
 * session keeping. Saves carry expected_version (409 on conflict).
 */

import { apiClient } from '../client'
import type { ResilienceExplanation } from './resilience'

export type ShareScheduler = 'sub2api' | 'share'

export interface ShareSchedulingSettings {
  enabled: boolean
}

export interface ShareGroupSummary {
  group_id: number
  name: string
  platform: string
  /** A stored `shadow` reads as `sub2api`. */
  scheduler: ShareScheduler
  /** True when the group has no policy yet. */
  session_keep: boolean
  /** The stored scheduler was the retired shadow mode. */
  legacy_shadow: boolean
  /** The group stores a policy. */
  configured: boolean
  /** The stored policy cannot be decoded; requests use the original scheduler. */
  invalid: boolean
  /** The main switch is on and the scheduler is share. */
  effective: boolean
  /** 0 when never saved. */
  version: number
  updated_at: string | null
  member_count: number
  available_count: number
  requests_1h: number
  cache_hit_ratio_1h: number | null
  switch_loss_ratio_1h: number | null
}

/** Why a member cannot take new sessions now. */
export type ShareMemberReason =
  | 'disabled'
  | 'inactive'
  | 'not_schedulable'
  | 'rate_limited'
  | 'overloaded'
  | 'temp_unschedulable'
  | 'balance_exhausted'
  | 'expired'
  | 'iq_degraded'
  | 'quota_exceeded'

export interface ShareMember {
  account_id: number
  name: string
  platform: string
  type: string
  status: string
  schedulable: boolean
  /** Can take new sessions now; per-request gates are not judged here. */
  healthy: boolean
  reason: ShareMemberReason | string | null
  recover_at: string | null
  /** The tier: lower numbers are used first. */
  priority: number
  concurrency: number
  /** The account's own load factor; null when unset. */
  load_factor: number | null
  /** The share weight: load factor, else concurrency, else 1. */
  effective_load_factor: number
  /** Other groups the account serves; a load factor change reaches them too. */
  other_group_ids: number[]
  /**
   * Expected share of new sessions under share scheduling. It depends only
   * on priorities, load factors and health, so it also tells an original
   * group what switching would do.
   */
  expected_share: number
  /** The expected share if only this account turned healthy. */
  share_if_healthy: number
  actual_requests_1h: number
  /** Share of the group's successful requests in the last hour. */
  actual_share_1h: number | null
  /** Successful requests of the account in every group, last hour. */
  recent_requests_1h: number
  /** Client-visible errors of the account in every group, last hour. */
  recent_errors_1h: number
  recent_error_rate_1h: number | null
}

export interface SharePolicyView {
  group_id: number
  group_name: string
  platform: string
  scheduler: ShareScheduler
  session_keep: boolean
  legacy_shadow: boolean
  effective: boolean
  version: number
  updated_by: number | null
  updated_at: string | null
  /** The main switch. */
  enabled: boolean
  /** Why the stored policy cannot be decoded. */
  invalid: string | null
  /** Lowest priority with a healthy member; null when none can take new sessions. */
  active_priority: number | null
  members: ShareMember[]
}

export interface ShareGroupUpdate {
  scheduler: ShareScheduler
  session_keep: boolean
  expected_version: number
}

export type ShareWarningCode =
  | 'global_switch_off'
  | 'no_healthy_account'
  | 'top_priority_unhealthy'
  | 'single_account_dominant'
  | 'high_load_factor'

/**
 * A preview warning. top_priority_unhealthy: priority is the top priority,
 * value the active one; single_account_dominant: value is the share;
 * high_load_factor: value is the load factor.
 */
export interface ShareWarning {
  code: ShareWarningCode | string
  account_id: number | null
  priority: number | null
  value: number | null
}

export interface SharePreview {
  scheduler: ShareScheduler
  session_keep: boolean
  enabled: boolean
  /** The group runs share scheduling after saving. */
  effective_after: boolean
  active_priority: number | null
  members: ShareMember[]
  /** Only for scheduler share; empty when going back to the original. */
  warnings: ShareWarning[]
}

export interface ShareExplainInput {
  model: string
  /** Empty simulates a new session with a random key. */
  session_hash?: string
  estimated_tokens?: number
  cache_one_hour?: boolean
  body?: unknown
}

export interface ShareExplainEntry {
  account_id: number
  priority: number
  /** The effective load factor. */
  load_factor: number
  eligible: boolean
  excluded: boolean
  reason?: string | null
  rank: number
  class?: string | null
  /** In-flight load against the load factor; null when it could not be read. */
  load_rate: number | null
  saturated: boolean
}

export interface ShareExplanation {
  group_id: number
  platform: string
  scheduler: ShareScheduler
  version: number
  /** No session hash was given: a random new session was simulated. */
  simulated_new_session: boolean
  key_source: string
  session?: string | null
  bound_account_id?: number | null
  idle_ms: number
  cache_ttl_seconds: number
  estimated_tokens: number
  cacheable: boolean
  warm: boolean
  wait_budget_ms: number
  max_waiting?: number
  reason: string
  steps: number[]
  write: string
  spill_write: string
  spill_reason?: string | null
  target?: number | null
  ideal?: number | null
  active_priority: number | null
  entries: ShareExplainEntry[]
  /** Gateway resilience view of the group and session. */
  resilience?: ResilienceExplanation
}

/** Health figures of one window; ratios are 0..1, null without samples. */
export interface ShareMetrics {
  requests: number
  cache_hit_ratio: number | null
  ttft_p50_ms: number | null
  ttft_p95_ms: number | null
  ttft_samples: number
  errors: number
  error_rate: number | null
  /** Sampled requests behind switch_rate and switch_loss_ratio (about 1 in 4 sessions). */
  attribution_samples: number
  /** Share of sampled requests that switched account while the cache was warm. */
  switch_rate: number | null
  /** Share of sampled input tokens that missed the cache because of a switch. */
  switch_loss_ratio: number | null
}

export type ShareBaselineUnavailable = 'not_share' | 'never_switched' | 'out_of_retention'

export interface ShareReportAccount {
  account_id: number
  requests: number
  request_share: number | null
  tokens: number
  token_share: number | null
  standard_cost: number
  cost_share: number | null
  /** Times passed over, by reason; share mode only. */
  skipped?: Record<string, number> | null
}

export interface ShareReportDetails {
  /** Both modes. */
  cache_classes: Record<string, { requests: number; tokens: number }>
  /** The rest is share mode only. */
  reasons?: Record<string, number> | null
  waits?: { count: number; hit: number; avg_ms: number } | null
  attempts?: number | null
  share_requests?: number | null
}

/** Where the switch time comes from: the switch record, or the policy's updated_at when the record is missing. */
export type ShareAnchorSource = 'switch_record' | 'policy_updated_at'

export interface ShareReport {
  group_id: number
  scheduler: ShareScheduler
  session_keep: boolean
  version: number
  window_seconds: number
  /** The current window; it starts at the switch when the switch falls inside the asked-for window. */
  current_from: string
  current_to: string
  current: ShareMetrics
  /** The latest scheduler change (sub2api ↔ share). */
  switched_at: string | null
  anchor_source: ShareAnchorSource | null
  /** The same length window before the switch; share mode only. */
  baseline_from: string | null
  baseline_to: string | null
  baseline: ShareMetrics | null
  baseline_unavailable: ShareBaselineUnavailable | null
  accounts: ShareReportAccount[]
  details: ShareReportDetails
  counters_error: string | null
  /** 'hour_rounded' for the baseline and windows over 2 hours. */
  counter_precision: 'minute' | 'hour_rounded'
}

/** An account's place in one group whose stored scheduler is share. */
export interface ShareAccountGroupRule {
  group_id: number
  group_name: string
  scheduler: ShareScheduler
  effective: boolean
  priority: number
  load_factor: number | null
  effective_load_factor: number
  healthy: boolean
  expected_share: number
  share_if_healthy: number
}

const base = '/admin/share-scheduling'

export async function getSettings(): Promise<ShareSchedulingSettings> {
  const { data } = await apiClient.get<ShareSchedulingSettings>(`${base}/settings`)
  return data
}

export async function updateSettings(settings: ShareSchedulingSettings): Promise<ShareSchedulingSettings> {
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

export async function updatePolicy(groupId: number, update: ShareGroupUpdate): Promise<SharePolicyView> {
  const { data } = await apiClient.put<SharePolicyView>(`${base}/groups/${groupId}/policy`, update)
  return data
}

export async function preview(groupId: number, update: Pick<ShareGroupUpdate, 'scheduler' | 'session_keep'>): Promise<SharePreview> {
  const { data } = await apiClient.post<SharePreview>(`${base}/groups/${groupId}/preview`, { scheduler: update.scheduler, session_keep: update.session_keep })
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

export async function accountRules(accountId: number): Promise<ShareAccountGroupRule[]> {
  const { data } = await apiClient.get<ShareAccountGroupRule[]>(`${base}/accounts/${accountId}/rules`)
  return data ?? []
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
  accountRules
}

export default shareSchedulingAPI
