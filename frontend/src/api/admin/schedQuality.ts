/**
 * Admin quality scheduling API (contract section 13), prefix /admin/sched-quality.
 */

import { apiClient } from '../client'
import type {
  SchedAccountDetail,
  SchedAccountsSummary,
  SchedAttention,
  SchedBoard,
  SchedClearAutoState,
  SchedGroupPolicy,
  SchedGroupPolicyUpdate,
  SchedOverrideRequest,
  SchedResetResult,
  SchedTestStart
} from '@/types/schedQuality'

const base = '/admin/sched-quality'

type RequestOptions = { signal?: AbortSignal }

/** Summary for the account list; at most 1000 accounts per call. */
export async function summary(accountIds: number[], model?: string, options?: RequestOptions): Promise<SchedAccountsSummary> {
  const { data } = await apiClient.post<SchedAccountsSummary>(
    `${base}/accounts/summary`,
    { account_ids: accountIds, ...(model ? { model } : {}) },
    { signal: options?.signal }
  )
  return data
}

/** IDs of accounts with a group in cooling. */
export async function attention(): Promise<SchedAttention> {
  const { data } = await apiClient.get<SchedAttention>(`${base}/accounts/attention`)
  return data
}

export async function accountDetail(id: number, options?: RequestOptions): Promise<SchedAccountDetail> {
  const { data } = await apiClient.get<SchedAccountDetail>(`${base}/accounts/${id}`, { signal: options?.signal })
  return data
}

export async function reset(id: number): Promise<SchedResetResult> {
  const { data } = await apiClient.post<SchedResetResult>(`${base}/accounts/${id}/reset`)
  return data
}

export async function setOverride(id: number, override: SchedOverrideRequest): Promise<void> {
  await apiClient.put(`${base}/accounts/${id}/override`, override)
}

/** groupId 0 cancels the override that covers every group. */
export async function clearOverride(id: number, groupId: number): Promise<void> {
  await apiClient.delete(`${base}/accounts/${id}/override`, { params: { group_id: groupId } })
}

/** Without a model the server picks up to 3 models of the account. */
export async function startTest(id: number, model?: string): Promise<SchedTestStart> {
  const { data } = await apiClient.post<SchedTestStart>(`${base}/accounts/${id}/test`, model ? { model } : {})
  return data
}

export async function clearAutoState(id: number, key: string): Promise<SchedClearAutoState> {
  const { data } = await apiClient.delete<SchedClearAutoState>(`${base}/accounts/${id}/auto-states/${encodeURIComponent(key)}`)
  return data
}

export async function getGroupPolicy(groupId: number): Promise<SchedGroupPolicy> {
  const { data } = await apiClient.get<SchedGroupPolicy>(`${base}/groups/${groupId}/policy`)
  return data
}

/** A version conflict is rejected with HTTP 409. */
export async function updateGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate): Promise<SchedGroupPolicy> {
  const { data } = await apiClient.put<SchedGroupPolicy>(`${base}/groups/${groupId}/policy`, update)
  return data
}

/** Without a model the server uses the model with the most samples in the group. */
export async function groupBoard(groupId: number, model?: string, options?: RequestOptions): Promise<SchedBoard> {
  const { data } = await apiClient.get<SchedBoard>(`${base}/groups/${groupId}/board`, {
    params: model ? { model } : undefined,
    signal: options?.signal
  })
  return data
}

export const schedQualityAPI = {
  summary,
  attention,
  accountDetail,
  reset,
  setOverride,
  clearOverride,
  startTest,
  clearAutoState,
  getGroupPolicy,
  updateGroupPolicy,
  groupBoard
}

export default schedQualityAPI
