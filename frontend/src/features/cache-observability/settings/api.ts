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

/** `afterRev` keeps only writes with `rev_after > afterRev`: the ones a conflict skipped over (§7.6). */
export async function listHistory(key: string, limit = 20, afterRev?: number): Promise<HistoryResponse> {
  const params: Record<string, string | number> = { key, limit }
  if (afterRev !== undefined) params.after_rev = afterRev
  const { data } = await apiClient.get<HistoryResponse>(`${base}/history`, { params })
  return data
}

/**
 * What `apiClient` rejects with for a non-2xx answer. The backend envelope is
 * `{code: <http status>, message, reason, metadata: map[string]string}` and
 * carries no `data` on errors, so the machine-readable code is `reason` and
 * every detail arrives as a string in `metadata` (CACHE_PANEL_API §7.5, M-1).
 * 401, 403 and 423 keep the platform shapes and are handled globally.
 */
interface RejectedApiError {
  status?: number
  reason?: string
  message?: string
  metadata?: Record<string, unknown>
}

export const POLICY_ERROR_REASONS = {
  conflict: 'CACHE_POLICY_REV_CONFLICT',
  invalid: 'CACHE_POLICY_INVALID',
  /** A value this build does not implement; `metadata.errors` as for invalid (F, M3: DESIGN §11.3, m3m4 README). */
  unsupported: 'CACHE_POLICY_UNSUPPORTED',
  confirm: 'CACHE_POLICY_CONFIRM_REQUIRED',
  notInBuild: 'CACHE_POLICY_NOT_IN_BUILD',
} as const

/** `metadata.current_rev` is a decimal string; anything else means "unknown". */
function parseRev(raw: unknown): number | null {
  if (typeof raw === 'number' && Number.isSafeInteger(raw) && raw >= 0) return raw
  if (typeof raw !== 'string' || !/^\d{1,15}$/.test(raw.trim())) return null
  return Number(raw.trim())
}

function toFieldError(raw: unknown): FieldError | null {
  if (!raw || typeof raw !== 'object') return null
  const item = raw as Record<string, unknown>
  if (typeof item.field !== 'string' || typeof item.code !== 'string') return null
  const error: FieldError = { field: item.field, code: item.code }
  if (typeof item.message === 'string') error.message = item.message
  if (item.limit && typeof item.limit === 'object') {
    const raw = item.limit as Record<string, unknown>
    const limit: NonNullable<FieldError['limit']> = {}
    if (typeof raw.min === 'number' && Number.isFinite(raw.min)) limit.min = raw.min
    if (typeof raw.max === 'number' && Number.isFinite(raw.max)) limit.max = raw.max
    error.limit = limit
  }
  return error
}

/** `metadata.errors` is a JSON string of `[{field, code, limit?: {min?, max?}}]`; unreadable input yields none. */
function parseFieldErrors(raw: unknown): FieldError[] {
  let list: unknown = raw
  if (typeof raw === 'string') {
    try {
      list = JSON.parse(raw)
    } catch {
      return []
    }
  }
  if (!Array.isArray(list)) return []
  return list.map(toFieldError).filter((e): e is FieldError => e !== null)
}

/** Maps a rejected write (validate or PUT) to the cases the panel explains to the admin. */
export function toWriteFailure(raw: unknown): WriteFailure {
  const error = (raw && typeof raw === 'object' ? raw : {}) as RejectedApiError
  const reason = typeof error.reason === 'string' ? error.reason : ''
  const metadata = error.metadata && typeof error.metadata === 'object' ? error.metadata : {}
  // `reason` decides; the HTTP status only covers a backend that omits it.
  if (reason === POLICY_ERROR_REASONS.conflict || (!reason && error.status === 409)) {
    return { kind: 'conflict', currentRev: parseRev(metadata.current_rev) }
  }
  if (reason === POLICY_ERROR_REASONS.invalid || reason === POLICY_ERROR_REASONS.unsupported || (!reason && error.status === 400)) {
    return { kind: 'invalid', errors: parseFieldErrors(metadata.errors) }
  }
  if (reason === POLICY_ERROR_REASONS.confirm || (!reason && error.status === 422)) {
    const missing = typeof metadata.missing === 'string' ? metadata.missing.split(',').map((m) => m.trim()).filter(Boolean) : []
    const typeToConfirm = typeof metadata.type_to_confirm === 'string' && metadata.type_to_confirm ? metadata.type_to_confirm : null
    return { kind: 'confirm', missing, typeToConfirm }
  }
  if (reason === POLICY_ERROR_REASONS.notInBuild || (!reason && error.status === 404)) return { kind: 'not_in_build' }
  return { kind: 'failed', message: typeof error.message === 'string' && error.message ? error.message : null }
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
