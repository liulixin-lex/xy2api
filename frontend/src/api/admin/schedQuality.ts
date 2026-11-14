/**
 * Admin quality scheduling API, prefix /admin/sched-quality.
 *
 * The UI is the v0.6.4 one; the backend answers in the 0.6.5 shapes. Every answer is turned into the types of
 * src/types/schedQuality.ts by ./schedQualityCompat before it leaves this module.
 */

import { apiClient } from '../client'
import type {
  SchedAccountDetail,
  SchedAccountsSummary,
  SchedBoard,
  SchedClearAutoState,
  SchedGroupPolicy,
  SchedGroupPolicyUpdate,
  SchedOverrideRequest,
  SchedPolicyPreview,
  SchedResetResult
} from '@/types/schedQuality'
import {
  toAccountDetail,
  toAccountsSummary,
  toBoard,
  toGroupPolicy,
  toResetResult,
  type BackendAccountDetail,
  type BackendAccountsSummary,
  type BackendGroupPolicy,
  type BackendOrder,
  type BackendResetResult
} from './schedQualityCompat'

const base = '/admin/sched-quality'

type RequestOptions = { signal?: AbortSignal }

// The account list reads the summary twice per refresh (quality capsules and the recent-stats column) for the same
// page of accounts. Concurrent calls for the same ids share one request; nothing is kept once it settles. Each caller
// keeps its own abort: an aborted caller is rejected at once, and the shared request is aborted when no caller is left.
interface SharedSummary { promise: Promise<BackendAccountsSummary>; controller: AbortController; waiting: number }
const summaryInFlight = new Map<string, SharedSummary>()

function abortError(): Error {
  const error = new Error('The operation was aborted')
  error.name = 'AbortError'
  return error
}

/** The backend's own summary of the accounts; at most 1000 per call. The account list's recent stats read it too. */
export function backendSummary(accountIds: number[], options?: RequestOptions): Promise<BackendAccountsSummary> {
  const signal = options?.signal
  if (signal?.aborted) return Promise.reject(abortError())
  const key = [...accountIds].sort((a, b) => a - b).join(',')
  let shared = summaryInFlight.get(key)
  if (!shared) {
    const controller = new AbortController()
    const promise = apiClient
      .post<BackendAccountsSummary>(`${base}/accounts/summary`, { account_ids: accountIds }, { signal: controller.signal })
      .then(({ data }) => data)
      .finally(() => { if (summaryInFlight.get(key)?.promise === promise) summaryInFlight.delete(key) })
    shared = { promise, controller, waiting: 0 }
    summaryInFlight.set(key, shared)
  }
  const entry = shared
  entry.waiting++
  return new Promise<BackendAccountsSummary>((resolve, reject) => {
    let done = false
    const leave = () => {
      if (done) return
      done = true
      signal?.removeEventListener('abort', onAbort)
      entry.waiting--
    }
    const onAbort = () => {
      if (done) return
      leave()
      if (entry.waiting === 0) {
        entry.controller.abort()
        if (summaryInFlight.get(key) === entry) summaryInFlight.delete(key)
      }
      reject(abortError())
    }
    signal?.addEventListener('abort', onAbort, { once: true })
    entry.promise.then(
      (data) => { if (!done) { leave(); resolve(data) } },
      (error) => { if (!done) { leave(); reject(error) } }
    )
  })
}

/** Summary for the account list; at most 1000 accounts per call. */
export async function summary(accountIds: number[], options?: RequestOptions): Promise<SchedAccountsSummary> {
  return toAccountsSummary(await backendSummary(accountIds, options))
}

export async function accountDetail(id: number, options?: RequestOptions): Promise<SchedAccountDetail> {
  const { data } = await apiClient.get<BackendAccountDetail>(`${base}/accounts/${id}`, { signal: options?.signal })
  return toAccountDetail(data)
}

export async function reset(id: number): Promise<SchedResetResult> {
  const { data } = await apiClient.post<BackendResetResult>(`${base}/accounts/${id}/reset`)
  return toResetResult(data)
}

/** The backend only takes `exempt`; the "demote" override was removed with 0.6.5. */
export async function setOverride(id: number, override: SchedOverrideRequest): Promise<void> {
  if (override.mode !== 'exempt') throw new Error(`Override mode "${override.mode}" is not supported by the backend`)
  await apiClient.put(`${base}/accounts/${id}/override`, override)
}

/** groupId 0 cancels the override that covers every group. */
export async function clearOverride(id: number, groupId: number): Promise<void> {
  await apiClient.delete(`${base}/accounts/${id}/override`, { params: { group_id: groupId } })
}

export async function clearAutoState(id: number, key: string): Promise<SchedClearAutoState> {
  const { data } = await apiClient.delete<SchedClearAutoState>(`${base}/accounts/${id}/auto-states/${encodeURIComponent(key)}`)
  return data
}

export async function getGroupPolicy(groupId: number): Promise<SchedGroupPolicy> {
  const { data } = await apiClient.get<BackendGroupPolicy>(`${base}/groups/${groupId}/policy`)
  return toGroupPolicy(data)
}

/** A version conflict is rejected with HTTP 409. */
export async function updateGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate): Promise<SchedGroupPolicy> {
  const { data } = await apiClient.put<BackendGroupPolicy>(`${base}/groups/${groupId}/policy`, update)
  return toGroupPolicy(data)
}

/**
 * What the policy would do if saved (backend 0.6.5): the group's normal / substandard counts now and after, and the
 * accounts it would turn substandard. Same body as the update; nothing is stored.
 */
export async function previewGroupPolicy(groupId: number, update: SchedGroupPolicyUpdate, options?: RequestOptions): Promise<SchedPolicyPreview> {
  const { data } = await apiClient.post<SchedPolicyPreview>(`${base}/groups/${groupId}/policy/preview`, update, { signal: options?.signal })
  return { now: data.now, after: data.after, changed: data.changed ?? [] }
}

/**
 * The group board, read from the backend's selection order (the /board route is gone). Without a model the server
 * uses the group's busiest model.
 */
export async function groupBoard(groupId: number, model?: string, options?: RequestOptions): Promise<SchedBoard> {
  const { data } = await apiClient.get<BackendOrder>(`${base}/groups/${groupId}/order`, {
    params: model ? { model } : undefined,
    signal: options?.signal
  })
  return toBoard(data)
}

export const schedQualityAPI = {
  summary,
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
