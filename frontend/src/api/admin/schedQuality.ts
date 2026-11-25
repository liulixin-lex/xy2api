/**
 * Admin quality scheduling API, prefix /admin/sched-quality (quality gate v2). Every answer is returned as the backend
 * sends it; the shapes are in ./schedQualityTypes.
 */

import { apiClient } from '../client'
import type {
  BackendAccountDetail,
  BackendAccountsSummary,
  BackendClearAutoState,
  BackendDecision,
  BackendGroupPolicy,
  BackendGroupPolicySaved,
  BackendGroupsSummary,
  BackendOrder,
  BackendPerformance,
  BackendPolicyPreview,
  BackendResetResult,
  BackendTTFTRule,
  BackendTTFTRuleSaved,
  SchedGroupPolicyUpdate,
  SchedOverrideRequest,
  SchedTTFTRuleUpdate
} from './schedQualityTypes'

const base = '/admin/sched-quality'

type RequestOptions = { signal?: AbortSignal }

/** The backend's own summary of the accounts (dots, recent status, attention); at most 1000 per call. */
export async function backendSummary(accountIds: number[], options?: RequestOptions): Promise<BackendAccountsSummary> {
  const { data } = await apiClient.post<BackendAccountsSummary>(`${base}/accounts/summary`, { account_ids: accountIds }, { signal: options?.signal })
  return data
}

/** One account's quality detail, in the backend's own shape (the quality panel reads it as is). */
export async function accountDetail(id: number, options?: RequestOptions): Promise<BackendAccountDetail> {
  const { data } = await apiClient.get<BackendAccountDetail>(`${base}/accounts/${id}`, { signal: options?.signal })
  return data
}

/** The account's identity changes, newest first; `beforeId` pages back from a record. */
export async function accountDecisions(
  id: number,
  page: { limit?: number; beforeId?: number } = {},
  options?: RequestOptions
): Promise<BackendDecision[]> {
  const params: Record<string, number> = { limit: page.limit ?? 50 }
  if (page.beforeId !== undefined) params.before_id = page.beforeId
  const { data } = await apiClient.get<BackendDecision[]>(`${base}/accounts/${id}/decisions`, { params, signal: options?.signal })
  return data ?? []
}

export async function reset(id: number): Promise<BackendResetResult> {
  const { data } = await apiClient.post<BackendResetResult>(`${base}/accounts/${id}/reset`)
  return data
}

/** An exemption for one group, or for every group (group_id 0). */
export async function setOverride(id: number, override: SchedOverrideRequest): Promise<void> {
  await apiClient.put(`${base}/accounts/${id}/override`, override)
}

/** groupId 0 cancels the override that covers every group. */
export async function clearOverride(id: number, groupId: number): Promise<void> {
  await apiClient.delete(`${base}/accounts/${id}/override`, { params: { group_id: groupId } })
}

export async function clearAutoState(id: number, key: string): Promise<BackendClearAutoState> {
  const { data } = await apiClient.delete<BackendClearAutoState>(`${base}/accounts/${id}/auto-states/${encodeURIComponent(key)}`)
  return data
}

export async function getGroupPolicy(groupId: number): Promise<BackendGroupPolicy> {
  const { data } = await apiClient.get<BackendGroupPolicy>(`${base}/groups/${groupId}/policy`)
  return data
}

/** A version conflict is rejected with HTTP 409. */
export async function updateGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate): Promise<BackendGroupPolicySaved> {
  const { data } = await apiClient.put<BackendGroupPolicySaved>(`${base}/groups/${groupId}/policy`, update)
  return data
}

/**
 * What the policy would say on the current evidence: per line the verdicts with the saved and the new thresholds, the
 * keys whose verdict turns, the lines it relaxes, and per model how many accounts reach its thresholds. Same body as
 * the update; nothing is stored.
 */
export async function previewGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate, options?: RequestOptions): Promise<BackendPolicyPreview> {
  const { data } = await apiClient.post<BackendPolicyPreview>(`${base}/groups/${groupId}/policy/preview`, update, { signal: options?.signal })
  return { relaxed_lines: data.relaxed_lines ?? [], lines: data.lines, changed: data.changed ?? [], reach: data.reach ?? [] }
}

/** Groups over all their models: identities of their account × model keys and the users' waits of the last hour. */
export async function groupsSummary(groupIds: number[], options?: RequestOptions): Promise<BackendGroupsSummary> {
  const { data } = await apiClient.post<BackendGroupsSummary>(`${base}/groups/summary`, { group_ids: groupIds }, { signal: options?.signal })
  return data
}

/**
 * The group board: the group's accounts in the backend's selection order for one request model (the group's busiest
 * without one), with the model's supply. Any model name may be asked for, also one missing from `models` (declared
 * only by accounts that are off and so without recent traffic).
 */
export async function groupBoard(groupId: number, model?: string, options?: RequestOptions): Promise<BackendOrder> {
  const { data } = await apiClient.get<BackendOrder>(`${base}/groups/${groupId}/order`, {
    params: model ? { model } : undefined,
    signal: options?.signal
  })
  return data
}

/** The group's accounts over the last 12 hours, every model (the board's 12-hour view). */
export async function groupPerformance(groupId: number, options?: RequestOptions): Promise<BackendPerformance> {
  const { data } = await apiClient.get<BackendPerformance>(`${base}/groups/${groupId}/performance`, { signal: options?.signal })
  return data
}

/** One account over the last 12 hours in each of its OpenAI groups, every model (the panel's 12-hour section). */
export async function accountPerformance(accountId: number, options?: RequestOptions): Promise<BackendPerformance> {
  const { data } = await apiClient.get<BackendPerformance>(`${base}/accounts/${accountId}/performance`, { signal: options?.signal })
  return data
}

/** The first-output line's three global settings, their defaults and the replayed ranges. */
export async function getTTFTRule(): Promise<BackendTTFTRule> {
  const { data } = await apiClient.get<BackendTTFTRule>(`${base}/ttft-rule`)
  return data
}

/** A version conflict is rejected with HTTP 409, an invalid value with 400 (INVALID_TTFT_RULE, nothing stored). */
export async function updateTTFTRule(update: SchedTTFTRuleUpdate): Promise<BackendTTFTRuleSaved> {
  const { data } = await apiClient.put<BackendTTFTRuleSaved>(`${base}/ttft-rule`, update)
  return data
}

export const schedQualityAPI = {
  accountDetail,
  accountDecisions,
  reset,
  setOverride,
  clearOverride,
  clearAutoState,
  getGroupPolicy,
  updateGroupPolicy,
  previewGroupPolicy,
  groupBoard,
  groupsSummary,
  groupPerformance,
  accountPerformance,
  getTTFTRule,
  updateTTFTRule
}

export default schedQualityAPI
