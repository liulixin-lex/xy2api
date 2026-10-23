// F's contract samples for /api/v1/admin/cache/policies (v1.7.4, C-1 to C-14),
// copied verbatim into ./contract. The backend handler tests use the same
// files; when a sample and CACHE_PANEL_API §7 disagree, the document wins and
// the sample is fixed upstream, then copied again.
import type { HistoryResponse, PoliciesResponse, PolicyEntry, ValidateResult } from '../types'

export interface ContractSample {
  _contract: string[]
  _note: string
  request: { method: string; path: string; body: unknown }
  status: number
  response: Record<string, any>
}

const files = import.meta.glob('./contract/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>

export const SAMPLE_NAMES = Object.keys(files).map((path) => path.replace('./contract/', '').replace('.json', ''))

export function sample(name: string): ContractSample {
  const found = files[`./contract/${name}.json`]
  if (!found) throw new Error(`unknown contract sample ${name}`)
  return structuredClone(found)
}

/** The `data` of a success sample, as `apiClient` resolves it. */
export function list(name: string): PoliciesResponse {
  return sample(name).response.data as PoliciesResponse
}

export function entryOf(name: string, key: string): PolicyEntry {
  const found = list(name).policies.find((p) => p.key === key)
  if (!found) throw new Error(`${name} has no ${key}`)
  return found
}

export function validation(name: string): ValidateResult {
  return sample(name).response.data as ValidateResult
}

export function history(name = 'history'): HistoryResponse {
  return sample(name).response.data as HistoryResponse
}

export function written(name: string): PolicyEntry {
  return sample(name).response.data as PolicyEntry
}

/**
 * What `apiClient` rejects with for an error sample. contract.spec checks this
 * against the real client, so unit tests that mock the API layer use the shape
 * the backend can actually produce (M-1).
 */
export function rejection(name: string) {
  const s = sample(name)
  const body = s.response
  return {
    status: s.status,
    code: body.code,
    reason: body.reason,
    error: body.error,
    message: body.message,
    metadata: body.metadata,
    retryAfterMs: undefined,
  }
}
