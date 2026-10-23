import { apiClient } from '@/api/client'
import type {
  FieldError,
  HistoryResponse,
  PoliciesResponse,
  PolicyEntry,
  PolicySpec,
  ValidateResult,
  WriteBody,
  WriteFailure,
} from './types'

const base = '/admin/cache/policies'

export async function listPolicies(): Promise<PoliciesResponse> {
  const { data } = await apiClient.get<PoliciesResponse>(base)
  return data
}

export async function getPolicy(key: string): Promise<PolicyEntry> {
  const { data } = await apiClient.get<PolicyEntry>(`${base}/${key}`)
  return data
}

export async function validatePolicy(key: string, body: WriteBody): Promise<ValidateResult> {
  const { data } = await apiClient.post<ValidateResult>(`${base}/${key}/validate`, body)
  return data
}

export async function putPolicy(key: string, body: WriteBody): Promise<PolicyEntry> {
  const { data } = await apiClient.put<PolicyEntry>(`${base}/${key}`, body)
  return data
}

export async function listHistory(key: string, limit = 20): Promise<HistoryResponse> {
  const { data } = await apiClient.get<HistoryResponse>(`${base}/history`, { params: { key, limit } })
  return data
}

interface RejectedApiError {
  status?: number
  code?: string
  message?: string
  metadata?: Record<string, unknown>
  data?: Record<string, unknown>
}

function detail(error: RejectedApiError): Record<string, unknown> {
  // The shared client forwards structured details as `metadata`; accept `data` too.
  return { ...(error.data ?? {}), ...(error.metadata ?? {}) }
}

/** Maps a rejected write (validate or PUT) to the cases the panel explains to the admin. */
export function toWriteFailure(raw: unknown): WriteFailure {
  const error = (raw && typeof raw === 'object' ? raw : {}) as RejectedApiError
  const extra = detail(error)
  if (error.status === 409 || error.code === 'CACHE_POLICY_REV_CONFLICT') {
    return { kind: 'conflict', currentRev: typeof extra.current_rev === 'number' ? extra.current_rev : null }
  }
  if (error.status === 400 || error.code === 'CACHE_POLICY_INVALID') {
    const errors = Array.isArray(extra.errors) ? (extra.errors as FieldError[]) : []
    return { kind: 'invalid', errors }
  }
  if (error.status === 422 || error.code === 'CACHE_POLICY_CONFIRM_REQUIRED') return { kind: 'confirm' }
  if (error.status === 404 || error.code === 'CACHE_POLICY_NOT_IN_BUILD') return { kind: 'not_in_build' }
  return { kind: 'failed', message: typeof error.message === 'string' ? error.message : null }
}

export function isNotFound(raw: unknown): boolean {
  return !!raw && typeof raw === 'object' && (raw as RejectedApiError).status === 404
}

export interface PolicyApi {
  listPolicies: typeof listPolicies
  getPolicy: typeof getPolicy
  validatePolicy: (key: string, body: WriteBody) => Promise<ValidateResult>
  putPolicy: (key: string, body: WriteBody) => Promise<PolicyEntry>
  listHistory: typeof listHistory
}

export const policyApi: PolicyApi = { listPolicies, getPolicy, validatePolicy, putPolicy, listHistory }

export type { PolicySpec }
