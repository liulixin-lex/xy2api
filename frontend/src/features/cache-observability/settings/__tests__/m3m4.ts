// F's M3 and M4 policy samples (00-shared/cache_policies_contract/m3m4, v1.1),
// copied verbatim into ./contract/m3m4 in the format of ./contract. The parent
// samples (v1.7.4.6) stay frozen; these add the upstream_identity,
// anthropic_identity and routing keys. When a sample and DESIGN §11 or
// CACHE_PANEL_API §7 disagree, the document wins and the sample is fixed
// upstream, then copied again.
import type { HistoryResponse, PoliciesResponse, PolicyEntry, ValidateResult, WriteBody } from '../types'
import type { ContractSample } from './contract'

const files = import.meta.glob('./contract/m3m4/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>

export const M3M4_NAMES = Object.keys(files).map((path) => path.replace('./contract/m3m4/', '').replace('.json', ''))

export function m3m4(name: string): ContractSample {
  const found = files[`./contract/m3m4/${name}.json`]
  if (!found) throw new Error(`unknown m3m4 sample ${name}`)
  return structuredClone(found)
}

/** The `data` of a success sample, as `apiClient` resolves it. */
export function m3m4List(name: string): PoliciesResponse {
  return m3m4(name).response.data as PoliciesResponse
}

export function m3m4Validation(name: string): ValidateResult {
  return m3m4(name).response.data as ValidateResult
}

export function m3m4Written(name: string): PolicyEntry {
  return m3m4(name).response.data as PolicyEntry
}

export function m3m4History(name: string): HistoryResponse {
  return m3m4(name).response.data as HistoryResponse
}

/** The body a validate or PUT sample sent: what the panel writes once the admin confirms. */
export function m3m4Body(name: string): WriteBody {
  return m3m4(name).request.body as WriteBody
}

/** What the preview sends to validate for the same draft: the rev and the changed fields only. */
export function m3m4Draft(name: string): Pick<WriteBody, 'expected_rev' | 'spec'> {
  const { expected_rev, spec } = m3m4Body(name)
  return { expected_rev, spec }
}

/** What `apiClient` rejects with for an error sample (as `rejection` in ./contract). */
export function m3m4Rejection(name: string) {
  const s = m3m4(name)
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
