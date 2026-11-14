/**
 * Compatibility layer: the v0.6.4 admin UI on the scheduling API of backend 0.6.5 and later.
 *
 * Backend 0.6.5 redesigned /admin/sched-quality. Three routes are gone (small tests, the group board, the recent
 * stats batch) and the remaining ones answer in a new shape. The UI in this tree is the v0.6.4 one and keeps its own
 * types (src/types/schedQuality.ts); everything it reads from these routes goes through the functions below.
 *
 *   - A value is carried over only when it means the same thing. What the backend no longer reports (p90 first
 *     output, per-model evidence cards, recent attempts, per-class limits, board evidence numbers) is null or empty,
 *     never estimated.
 *   - What the backend no longer does (small tests, the "demote" override) has no entry point left in the UI.
 *
 * Wording differs between the two generations and is mapped as: substandard (不达标) -> cooling (冷却),
 * exemption (豁免) -> the exempt override, the "stopped" layer of the selection order -> not schedulable (停调).
 */

import type {
  AccountRecentStatsBatch,
  RecentStatsTier,
  SchedAccountSummary,
  SchedAccountsSummary,
  SchedBoard,
  SchedBoardAccount,
  SchedGroupPolicy,
  SchedPolicy,
  SchedReason,
  SchedResetResult,
  SchedVerdict
} from '@/types/schedQuality'

// ---- What the backend sends (backend/internal/service/sched_admin_dto.go at 0.6.5) ----

import type { BackendAccountSummary, BackendAccountsSummary, BackendDim, BackendExemption, BackendMark, BackendSubstandard } from './schedQualityTypes'

export type { BackendAccountSummary, BackendAccountsSummary, BackendDim, BackendExemption, BackendMark, BackendSubstandard }

export interface BackendResetResult {
  account_id: number
  reset_at: string | null
  cleared: string[]
  kept: { kind: string; until: string | null }[]
  failed: { kind: string }[]
}

export interface BackendGroupPolicy {
  group_id: number
  version: number
  policy: SchedPolicy
}

export interface BackendOrderRow {
  rank: number
  account_id: number
  name: string
  /** normal | below | stopped */
  layer: string
  dims: BackendDim[]
  until: string | null
  exempt: boolean
  priority: number
  rate: number
  load_rate: number
  in_flight: number
  concurrency: number
  last_used_at: string | null
}

export interface BackendOrder {
  group_id: number
  model: string
  models: string[]
  at: string
  stale: boolean
  global_enabled: boolean
  rows: BackendOrderRow[]
}

// ---- Shared pieces ----

const REASON_OF_DIM: Record<BackendDim, SchedReason> = { ttfo: 'speed', error: 'error', cache: 'cache' }

/** The cooling reason shown for a substandard row: its first failing dimension. */
const reasonOf = (dims: BackendDim[]): SchedReason => REASON_OF_DIM[dims[0]] ?? ''

// ---- POST /accounts/summary ----

/**
 * The backend no longer counts an account's groups. `groups` keeps the two facts the list reads from it: how many
 * groups are substandard (the recovery entry), and whether the account has any sample at all (`正常` or `暂无数据`).
 */
export function toAccountSummary(account: BackendAccountSummary): SchedAccountSummary {
  // One capsule per group: of several substandard models, the one whose round ends last stands for the group.
  const byGroup = new Map<number, BackendSubstandard>()
  for (const item of account.substandard) {
    const seen = byGroup.get(item.group_id)
    if (!seen || Date.parse(item.until) > Date.parse(seen.until)) byGroup.set(item.group_id, item)
  }
  const cooling = [...byGroup.values()]
    .sort((a, b) => a.group_id - b.group_id)
    .map((item) => ({ group_id: item.group_id, model: item.model, reason: reasonOf(item.dims), until: item.until, waiting_test: false, testing: false }))
  const samples = Math.max(account.ttfo.n, account.error.n, account.cache.n)
  const hasData = samples > 0 || cooling.length > 0
  const total = Math.max(cooling.length, 1)
  return {
    account_id: account.account_id,
    model: cooling[0]?.model ?? null,
    samples,
    // The backend reports a median now, not the p90 this field held.
    ttfo: { p90_ms: null, timeout: false, n: account.ttfo.n },
    error: { rate: account.error.rate, n: account.error.n },
    cache: { ratio: account.cache.ratio, n: account.cache.n },
    groups: { total, normal: hasData ? total - cooling.length : 0, cooling: cooling.length, nodata: hasData ? 0 : total },
    cooling,
    cooling_more: 0,
    overrides: [...account.exemptions]
      .sort((a, b) => a.group_id - b.group_id)
      .map((exemption) => ({ group_id: exemption.group_id, mode: 'exempt' as const, expires_at: exemption.expires_at }))
  }
}

export function toAccountsSummary(summary: BackendAccountsSummary): SchedAccountsSummary {
  return { at: summary.at, stale: summary.stale, global_enabled: summary.global_enabled, accounts: summary.accounts.map(toAccountSummary) }
}

// ---- Recent stats of the account list, read from the same summary ----

/** The backend only tells which dimension is over a line: that one is `bad`; it grades nothing as good or mid. */
const tierOf = (mark: BackendMark): RecentStatsTier => (mark.red ? 'bad' : null)

export function toRecentStats(summary: BackendAccountsSummary): AccountRecentStatsBatch {
  return {
    as_of: summary.at,
    items: summary.accounts.map((account) => ({
      account_id: account.account_id,
      ttft: { p50_ms: account.ttfo.p50_ms, n: account.ttfo.n, tier: tierOf(account.ttfo) },
      error: {
        rate: account.error.rate,
        bad: account.error.rate === null ? 0 : Math.round(account.error.rate * account.error.n),
        n: account.error.n,
        tier: tierOf(account.error)
      },
      cache: { ratio: account.cache.ratio, n: account.cache.n, tier: tierOf(account.cache) }
    }))
  }
}

// ---- POST /accounts/:id/reset ----

export function toResetResult(result: BackendResetResult): SchedResetResult {
  return {
    reset_at: result.reset_at,
    cleared: result.cleared.map((kind) => ({ key: kind, kind })),
    preserved: result.kept.map((item) => ({ key: item.kind, kind: item.kind, why: '' })),
    failed: result.failed.map((item) => ({ key: item.kind, kind: item.kind, error: '' }))
  }
}

// ---- GET | PUT /groups/:id/policy ----

/** Per-class limits are gone with the request classes. */
export function toGroupPolicy(policy: BackendGroupPolicy): SchedGroupPolicy {
  return { group_id: policy.group_id, version: policy.version, policy: policy.policy, class_limits_ms: {} }
}

// ---- The group board, read from GET /groups/:id/order ----

function toBoardAccount(row: BackendOrderRow, model: string): SchedBoardAccount {
  const stopped = row.layer === 'stopped'
  const below = row.layer === 'below'
  const verdict = (dim: BackendDim): SchedVerdict => (row.dims.includes(dim) ? 'fail' : 'nodata')
  return {
    account_id: row.account_id,
    name: row.name,
    evidence_model: model,
    rank: row.rank,
    eligible: !stopped,
    reject: stopped ? 'not_schedulable' : '',
    reject_until: stopped ? row.until : null,
    standing: below ? 'back' : 'front',
    state: below ? 'cooling' : 'normal',
    level: 0,
    until: stopped ? null : row.until,
    waiting_test: false,
    testing: false,
    override: row.exempt ? { mode: 'exempt', all_groups: false, expires_at: null, reason: '' } : null,
    priority: row.priority,
    rate: row.rate,
    in_flight: row.in_flight,
    concurrency: row.concurrency,
    recently_full_seconds: 0,
    // Not reported any more; true keeps the "no takeover" mark off instead of guessing it.
    can_take_over: true,
    verdicts: { ttfo: verdict('ttfo'), error: verdict('error'), cache: verdict('cache') },
    ttfo: { n: 0, sessions: 0, p90_ms: null, p90_timeout: false, fail_bound_ms: null, fail_bound_timeout: false },
    error: { n: 0, sessions: 0, rate: null, lcb: null },
    cache: { n: 0, sessions: 0, ratio: null }
  }
}

export function toBoard(order: BackendOrder): SchedBoard {
  return {
    group_id: order.group_id,
    model: order.model,
    models: order.models,
    at: order.at,
    stale: order.stale,
    global_enabled: order.global_enabled,
    accounts: order.rows.map((row) => toBoardAccount(row, order.model))
  }
}

// ---- GET /admin/accounts?sched_quality= ----

/**
 * The value the account list accepts now: normal | substandard | exempt | observing. `cooling` is `substandard`;
 * `demote` (an old saved link) has no counterpart and filters nothing. Anything else is passed on as it is.
 */
export function toQualityFilter<T extends string | null | undefined>(value: T): T | string {
  if (value === 'cooling') return 'substandard'
  if (value === 'demote') return ''
  return value
}

/** Filters of an account list request with the quality value translated. */
export function withQualityFilter<T extends { sched_quality?: string }>(filters: T | undefined): T | undefined {
  if (!filters || filters.sched_quality === undefined) return filters
  return { ...filters, sched_quality: toQualityFilter(filters.sched_quality) }
}
