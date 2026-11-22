/**
 * Synthetic 12-hour performance rows (GET /admin/sched-quality/groups/:id/performance and accounts/:id/performance)
 * for tests. Counts are shaped after the round-3 review's table (only the shape: no real account, group or model).
 */
import type { BackendLastEval, BackendPerfGroup, BackendPerfRow, BackendPerformance } from '@/api/admin/schedQualityTypes'

export const perfGroup = (patch: Partial<BackendPerfGroup> = {}): BackendPerfGroup => ({
  group_id: 18, name: '晨星组', ttft_seconds: 8, min_cache_ratio: 0.8, gate_enabled: true, ...patch
})

type RowPatch = Omit<Partial<BackendPerfRow>, 'first' | 'cont' | 'attempts' | 'score' | 'switches'> & {
  first?: Partial<BackendPerfRow['first']>
  cont?: Partial<BackendPerfRow['cont']>
  attempts?: Partial<BackendPerfRow['attempts']>
  score?: Partial<BackendPerfRow['score']>
  switches?: Partial<BackendPerfRow['switches']>
}

/**
 * A row: `first.n` first sends of which `slowRate` were slow against `expectedRate` expected (every one comparable),
 * 30 sessions; 210 continued turns at 90% cache; 480 attempts, 19 timeouts; every line unknown, never assessed.
 */
export function perfRow(id: number, patch: RowPatch = {}, rates: { n?: number; slowRate?: number; expectedRate?: number | null } = {}): BackendPerfRow {
  const n = rates.n ?? 463
  const expectedRate = rates.expectedRate === undefined ? 0.197 : rates.expectedRate
  const line = { identity: 'unknown' as const, level: 0, until: null, trial_started_at: null, last_eval: null }
  return {
    group_id: 18,
    account_id: id,
    name: `acc-${id}`,
    model: 'gpt-x',
    priority: 5,
    member: true,
    requests: n,
    ...patch,
    first: {
      n, slow: Math.round(n * (rates.slowRate ?? 0.2)), sessions: 30, expected_n: expectedRate === null ? 0 : n,
      expected_slow: expectedRate === null ? null : n * expectedRate, unknown: 0, ...patch.first
    },
    cont: { turns: 210, sessions: 12, read_tokens: 900, input_tokens: 1000, ...patch.cont },
    attempts: { n: 480, timeouts: 19, failures: 0, ...patch.attempts },
    switches: { handoff: 0, takeover: 0, migrate: 0, profit_floor: 0, other: 0, ...patch.switches },
    score: { value: null, n: 0, sessions: 0, excess: 0, ...patch.score },
    lines: patch.lines ?? { ttfo: { ...line }, error: { ...line }, cache: { ...line } }
  }
}

export const lastEval = (patch: Partial<BackendLastEval> = {}): BackendLastEval => ({
  at: '2026-11-19T12:05:00Z', acted: false, action: '', reason: 'peer_short', causes: ['peer_known_insufficient'], have: 8, need: 20,
  since: '2026-11-19T09:15:00Z', count: 35, ...patch
})

export const perfAnswer = (rows: BackendPerfRow[], groups: BackendPerfGroup[] = [perfGroup()], patch: Partial<BackendPerformance> = {}): BackendPerformance => ({
  at: '2026-11-19T12:10:00Z', window_from: '2026-11-19T00:10:00Z', window_hours: 12, complete: true, stale: false, global_enabled: true,
  groups, rows, ...patch
})
