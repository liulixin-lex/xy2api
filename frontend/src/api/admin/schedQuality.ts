/**
 * Admin quality scheduling API, prefix /admin/sched-quality (backend 0.6.5). Every answer is returned as the backend
 * sends it; the shapes are in ./schedQualityTypes.
 */

import { apiClient } from '../client'
import type {
  BackendAccountDetail,
  BackendAccountsSummary,
  BackendClearAutoState,
  BackendGroupPolicy,
  BackendOrder,
  BackendPolicyPreview,
  BackendResetResult,
  SchedGroupPolicyUpdate,
  SchedOverrideRequest
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
export async function updateGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate): Promise<BackendGroupPolicy> {
  const { data } = await apiClient.put<BackendGroupPolicy>(`${base}/groups/${groupId}/policy`, update)
  return data
}

/**
 * What the policy would do if saved (backend 0.6.5): the group's normal / substandard counts now and after, and the
 * accounts it would turn substandard. Same body as the update; nothing is stored.
 */
export async function previewGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate, options?: RequestOptions): Promise<BackendPolicyPreview> {
  const { data } = await apiClient.post<BackendPolicyPreview>(`${base}/groups/${groupId}/policy/preview`, update, { signal: options?.signal })
  return { now: data.now, after: data.after, changed: data.changed ?? [] }
}

/**
 * The group board: the group's accounts in the backend's selection order for one request model (the group's busiest
 * without one).
 */
export async function groupBoard(groupId: number, model?: string, options?: RequestOptions): Promise<BackendOrder> {
  const { data } = await apiClient.get<BackendOrder>(`${base}/groups/${groupId}/order`, {
    params: model ? { model } : undefined,
    signal: options?.signal
  })
  return data
}

export const schedQualityAPI = {
  accountDetail,
  reset,
  setOverride,
  clearOverride,
  clearAutoState,
  getGroupPolicy,
  updateGroupPolicy,
  previewGroupPolicy,
  groupBoard
}

export default schedQualityAPI
