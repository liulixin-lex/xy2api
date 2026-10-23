// Unit-test fixtures derived from the contract samples, so every shape is one
// the backend produces; variations only change values.
import type { AvailablePolicy, PoliciesResponse, PolicyEntry, PolicySpec, ValidateResult } from '../types'
import { entryOf, list, validation as sampleValidation } from './contract'

/** M1 build, never written: master and observability at rev 0 with defaults, the other four not in the build. */
export function m1Response(options: { kill?: boolean } = {}): PoliciesResponse {
  const response = list('list_default_all_off')
  response.static.kill_switch = options.kill ?? false
  return response
}

/** M3 build: upstream_identity configured (uuid_fill, 50%, two accounts) and masked by the master switch. */
export function m3Response(): PoliciesResponse {
  return list('list_m3_masked_by_master')
}

export function observabilityEntry(): AvailablePolicy {
  return entryOf('list_default_all_off', 'observability') as AvailablePolicy
}

export function identityEntry(): AvailablePolicy {
  return entryOf('list_m3_masked_by_master', 'upstream_identity') as AvailablePolicy
}

/** The same entry after someone wrote `rev` with `spec` merged over the stored value. */
export function rewritten<T extends AvailablePolicy>(e: T, rev: number, spec: PolicySpec, by = 'oncall@example.com', reason: string | null = null): T {
  return {
    ...e,
    stored: { ...e.stored, rev, updated_at: '2026-10-23T08:58:10Z', updated_by: { user_id: 2, email: by }, reason, spec: { ...e.stored.spec, ...spec } },
    loaded: { rev, loaded_at: '2026-10-23T08:58:10Z' },
  }
}

export function withEntry(response: PoliciesResponse, next: PolicyEntry): PoliciesResponse {
  return { ...response, policies: response.policies.map((p) => (p.key === next.key ? next : p)) }
}

/** A passing low-risk enable of observability (validate_ok_observability_enable), with overrides. */
export function validation(overrides: Partial<ValidateResult> = {}): ValidateResult {
  return { ...sampleValidation('validate_ok_observability_enable'), ...overrides }
}
