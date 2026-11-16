/**
 * Synthetic quality-gate v2 detail rows (GET /admin/sched-quality/accounts/:id `by_model[]`) for tests: each line's
 * identity, a plausible evidence object and the placement the identities add up to. No real data.
 */
import type { BackendDim, BackendEvidenceOf, BackendGroupModel, BackendIdentity, BackendLine } from '@/api/admin/schedQualityTypes'

export const ttfoEvidence = (patch: Partial<BackendEvidenceOf['ttfo']> = {}): BackendEvidenceOf['ttfo'] => ({
  limit_s: 10, n: 24.5, k: 1.2, x: 0.5, s: 3, d: 1, prior_mu: 0.08, peer_nu: 0.09, p_abs: 0.02, p_rel: 0.01, u: 0.12, z: 0.17, causes: [], ...patch
})
export const errorEvidence = (patch: Partial<BackendEvidenceOf['error']> = {}): BackendEvidenceOf['error'] => ({
  limit: 0.05, clear: 80, failed: 1, unknown: 2, padded: 82, lower_fail: 0.002, lower_pass: 0.012, ...patch
})
export const cacheEvidence = (patch: Partial<BackendEvidenceOf['cache']> = {}): BackendEvidenceOf['cache'] => ({
  min_ratio: 0.8, turns: 30, ratio: 0.91, sessions: 6, low_sessions: 0, ...patch
})

const EVIDENCE = { ttfo: ttfoEvidence, error: errorEvidence, cache: cacheEvidence }
const VERDICTS = { pass: 'pass', rear: 'fail', unknown: 'unknown', trial: 'unknown' } as const

export function line<D extends BackendDim>(dim: D, identity: BackendIdentity = 'pass', patch: Partial<BackendLine<D>> = {}): BackendLine<D> {
  return {
    identity,
    level: 0,
    until: null,
    trial_until: null,
    verdict: VERDICTS[identity],
    evidence: EVIDENCE[dim]() as BackendEvidenceOf[D],
    source_groups: [],
    ...patch
  }
}

/** A model row; lines not named pass. The placement follows the backend's rule: any rear → rear, any trial → trial. */
export function groupModel(model: string, identities: Partial<Record<BackendDim, BackendIdentity>> = {}, patch: Partial<BackendGroupModel> = {}): BackendGroupModel {
  const values = Object.values(identities)
  return {
    model,
    placement: values.includes('rear') ? 'rear' : values.includes('trial') ? 'trial' : 'front',
    exempt: false,
    lines: { ttfo: line('ttfo', identities.ttfo), error: line('error', identities.error), cache: line('cache', identities.cache) },
    ...patch
  }
}
