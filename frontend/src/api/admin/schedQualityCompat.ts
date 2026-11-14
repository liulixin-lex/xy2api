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
  SchedAccountDetail,
  SchedAccountGroup,
  SchedAccountSummary,
  SchedAccountsSummary,
  SchedAutoState,
  SchedBoard,
  SchedBoardAccount,
  SchedGroupModelRow,
  SchedGroupPolicy,
  SchedModelEvidence,
  SchedOverride,
  SchedPolicy,
  SchedReason,
  SchedResetResult,
  SchedVerdict
} from '@/types/schedQuality'

// ---- What the backend sends (backend/internal/service/sched_admin_dto.go at 0.6.5) ----

export type BackendDim = 'ttfo' | 'error' | 'cache'

interface BackendMark {
  n: number
  /** Below the sample minimum. */
  short: boolean
  /** This dimension makes the account substandard in some group. */
  red: boolean
}

export interface BackendExemption {
  /** 0 = every group. */
  group_id: number
  expires_at: string | null
  reason: string
}

export interface BackendSubstandard {
  group_id: number
  model: string
  dims: BackendDim[]
  until: string
  other_group: boolean
  exempt: boolean
}

export interface BackendAccountSummary {
  account_id: number
  status: string
  reset_at: string | null
  stop: { source: string; reason: string; until: string | null } | null
  ttfo: BackendMark & { p50_ms: number | null }
  error: BackendMark & { rate: number | null }
  cache: BackendMark & { ratio: number | null }
  substandard: BackendSubstandard[]
  exemptions: BackendExemption[]
}

export interface BackendAccountsSummary {
  at: string
  stale: boolean
  global_enabled: boolean
  accounts: BackendAccountSummary[]
}

interface BackendDimDetail {
  scope: string
  window_min: number
  n: number
  lcb: number | null
  /** pass | fail | short */
  verdict: string
}

export interface BackendGroupModelDetail {
  model: string
  failing: boolean
  dims: BackendDim[]
  level: number
  since: string | null
  until: string | null
  other_group_ids: number[]
  exempt: boolean
  ttfo: BackendDimDetail & { bad: number }
  error: BackendDimDetail & { bad: number }
  cache: BackendDimDetail & { ratio: number | null }
}

export interface BackendAccountDetail {
  account: SchedAccountDetail['account']
  at: string
  stale: boolean
  global_enabled: boolean
  status: string
  stop: BackendAccountSummary['stop']
  reset_at: string | null
  groups: {
    group_id: number
    name: string
    policy: SchedPolicy
    exemption: BackendExemption | null
    by_model: BackendGroupModelDetail[]
  }[]
  auto_states: SchedAutoState[]
  exemptions: BackendExemption[]
}

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

/** `short` (too few samples) is the old `nodata`; a word this UI does not know passes through and reads as unknown. */
const verdictOf = (verdict: string): SchedVerdict => (verdict === 'short' ? 'nodata' : (verdict as SchedVerdict))

const toOverride = (exemption: BackendExemption): SchedOverride => ({
  mode: 'exempt',
  all_groups: exemption.group_id === 0,
  expires_at: exemption.expires_at,
  reason: exemption.reason
})

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

// ---- GET /accounts/:id ----

function toGroupModelRow(row: BackendGroupModelDetail, policy: SchedPolicy): SchedGroupModelRow {
  const from = (n: number) => (n > 0 ? ('real' as const) : null)
  return {
    model: row.model,
    state: row.failing ? 'cooling' : 'normal',
    level: row.level,
    reason: row.failing ? reasonOf(row.dims) : '',
    since: row.since,
    until: row.until,
    waiting_test: false,
    testing: false,
    // First output is judged by the share of requests over the line now; there is no p90 to show.
    ttfo: { verdict: verdictOf(row.ttfo.verdict), from: from(row.ttfo.n), n: row.ttfo.n, p90_ms: null, p90_timeout: false, limit_ms: Math.round(policy.ttft_seconds * 1000) },
    error: { verdict: verdictOf(row.error.verdict), from: from(row.error.n), n: row.error.n, rate: row.error.n > 0 ? row.error.bad / row.error.n : null, limit: policy.max_error_rate },
    cache: { verdict: verdictOf(row.cache.verdict), from: from(row.cache.n), n: row.cache.n, ratio: row.cache.ratio, limit: policy.min_cache_ratio }
  }
}

/** A model tab without numbers: evidence is kept per group and model now, not per account and model. */
const emptyEvidence = (model: string): SchedModelEvidence => ({
  model,
  request_model: model,
  samples: 0,
  sessions: 0,
  from: null,
  to: null,
  ttfo: { n: 0, sessions: 0, timeouts: 0, p90_ms: null, p90_timeout: false, fail_bound_ms: null, fail_bound_timeout: false, by_class: [] },
  error: { n: 0, sessions: 0, bad: 0, rate: null, lcb: null },
  cache: { turns: 0, sessions: 0, ratio: null },
  test: null
})

export function toAccountDetail(detail: BackendAccountDetail): SchedAccountDetail {
  const groups: SchedAccountGroup[] = detail.groups.map((group) => ({
    group_id: group.group_id,
    name: group.name,
    gate_enabled: group.policy.enabled,
    policy: {
      ttft_seconds: group.policy.ttft_seconds,
      max_error_rate: group.policy.max_error_rate,
      min_cache_ratio: group.policy.min_cache_ratio,
      takeover_idle_seconds: group.policy.takeover_idle_seconds
    },
    override: group.exemption ? toOverride(group.exemption) : null,
    by_model: group.by_model.map((row) => toGroupModelRow(row, group.policy))
  }))
  // The tabs: every model some group has a row for, in first-seen order.
  const models = [...new Set(groups.flatMap((group) => group.by_model.map((row) => row.model)))]
  const allGroups = detail.exemptions.find((exemption) => exemption.group_id === 0)
  return {
    account: detail.account,
    at: detail.at,
    stale: detail.stale,
    global_enabled: detail.global_enabled,
    tests_enabled: false,
    reset_at: detail.reset_at,
    models: models.map(emptyEvidence),
    groups,
    all_groups_override: allGroups ? toOverride(allGroups) : null,
    auto_states: detail.auto_states,
    recent_attempts: []
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
