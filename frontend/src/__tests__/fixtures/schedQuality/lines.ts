/**
 * Synthetic quality-gate v2 detail rows (GET /admin/sched-quality/accounts/:id `by_model[]`) for tests: each line's
 * identity, a plausible evidence object with this group's and all the counted units, and the placement the identities
 * add up to. `legacyLine` is a record from before those were kept (the new fields absent). No real data.
 */
import type { BackendBasis, BackendDim, BackendEvidenceOf, BackendGroupModel, BackendIdentity, BackendLine } from '@/api/admin/schedQualityTypes'

export const ttfoEvidence = (patch: Partial<BackendEvidenceOf['ttfo']> = {}): BackendEvidenceOf['ttfo'] => ({
  limit_s: 10, n: 24.5, k: 1.2, x: 0.5, n_local: 9.5, s: 3, d: 1, prior_mu: 0.08, peer_nu: 0.09, p_abs: 0.02, p_rel: 0.01, u: 0.12, z: 0.17, causes: [], ...patch
})
export const errorEvidence = (patch: Partial<BackendEvidenceOf['error']> = {}): BackendEvidenceOf['error'] => ({
  limit: 0.05, clear: 80, failed: 1, unknown: 2, padded: 82, lower_fail: 0.002, lower_pass: 0.012, ...patch
})
export const cacheEvidence = (patch: Partial<BackendEvidenceOf['cache']> = {}): BackendEvidenceOf['cache'] => ({
  min_ratio: 0.8, turns: 30, ratio: 0.91, sessions: 6, low_sessions: 0, ...patch
})

const EVIDENCE = { ttfo: ttfoEvidence, error: errorEvidence, cache: cacheEvidence }
/** This group's and all the counted units of each line (samples, clear outcomes, turns); borrowed ones only in all. */
export const SAMPLES: Record<BackendDim, { samples_local: number; samples_total: number }> = {
  ttfo: { samples_local: 11, samples_total: 30 },
  error: { samples_local: 40, samples_total: 80 },
  cache: { samples_local: 12, samples_total: 30 }
}
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
    ...SAMPLES[dim],
    ...patch
  }
}

/** A line written before the per-group counts were kept: no samples_local / samples_total, no n_local. */
export function legacyLine<D extends BackendDim>(dim: D, identity: BackendIdentity = 'pass'): BackendLine<D> {
  const { samples_local: _local, samples_total: _total, ...rest } = line(dim, identity)
  const { n_local: _weight, ...evidence } = rest.evidence as BackendEvidenceOf['ttfo']
  return { ...rest, evidence: (dim === 'ttfo' ? evidence : rest.evidence) as BackendEvidenceOf[D] }
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

/** A decision's basis (GET …/accounts/:id/decisions) with this group's and all the counted units of its line. */
export function basis(dim: BackendDim, patch: Partial<BackendBasis> = {}): BackendBasis {
  const evidence = { ttfo: { ttft: ttfoEvidence() }, error: { error: errorEvidence() }, cache: { cache: cacheEvidence() } }[dim]
  return { rule_version: '3.0', line: dim, kind: 'regular', threshold: 0.05, source_groups: [53], ...evidence, ...SAMPLES[dim], ...patch }
}

/** An older decision's basis: written before the counts, n_local and trial_seats were kept (the fields absent). */
export function legacyBasis(dim: BackendDim, patch: Partial<BackendBasis> = {}): BackendBasis {
  const { samples_local: _local, samples_total: _total, ...rest } = basis(dim, patch)
  if (rest.ttft) {
    const { n_local: _weight, ...ttft } = rest.ttft
    rest.ttft = ttft
  }
  return rest
}
