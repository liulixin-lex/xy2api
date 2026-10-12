/**
 * Share scheduling editor helpers. The draft keeps one row per group member;
 * only configured rows are saved as account rules, the rest follow the policy
 * defaults (tier = account priority, weight = concurrency or 1), mirroring
 * sharesched.Policy.ResolveRule on the backend.
 */

import type { ShareAccountRule, ShareMember, SharePolicy, SharePolicyView } from '@/api/admin/shareScheduling'

export const SHARE_MIN_WEIGHT = 1
export const SHARE_MAX_WEIGHT = 1_000_000
export const SHARE_MAX_CACHE_DOMAIN = 64
const TIER_MIN = -2147483648
const TIER_MAX = 2147483647
const cacheDomainPattern = /^[A-Za-z0-9._:-]+$/
/** Prefix of the implicit per-account domains, reserved by the backend. */
const OWN_CACHE_DOMAIN_PREFIX = 'account:'

export interface ShareDraftRow {
  account_id: number
  name: string
  platform: string
  type: string
  priority: number
  concurrency: number
  configured: boolean
  tier: number
  weight: number
  disabled: boolean
  drain_fallback: boolean
  drain: boolean
  drain_until: string
  cache_domain: string
}

export interface ShareDraft {
  policy: SharePolicy
  rows: ShareDraftRow[]
  /** Rules naming accounts that left the group; they are dropped on save. */
  stale: ShareAccountRule[]
}

export interface ShareProblem {
  key: string
  params?: Record<string, string | number>
}

/** Deep-copies JSON data; unlike structuredClone it accepts reactive proxies. */
export function cloneShareJSON<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

export function shareDefaultRule(policy: SharePolicy, member: Pick<ShareMember, 'priority' | 'concurrency'>): { tier: number; weight: number } {
  const weight = policy.defaults.weight === 'concurrency'
    ? Math.min(Math.max(member.concurrency, SHARE_MIN_WEIGHT), SHARE_MAX_WEIGHT)
    : 1
  return { tier: member.priority, weight }
}

export function draftFromView(view: SharePolicyView): ShareDraft {
  const policy = cloneShareJSON(view.policy)
  const rules = new Map(policy.accounts.map(rule => [rule.account_id, rule]))
  const rows = view.members.map((member): ShareDraftRow => {
    const rule = rules.get(member.account_id)
    const fallback = shareDefaultRule(policy, member)
    return {
      account_id: member.account_id,
      name: member.name,
      platform: member.platform,
      type: member.type,
      priority: member.priority,
      concurrency: member.concurrency,
      configured: Boolean(rule),
      tier: rule?.tier ?? fallback.tier,
      weight: rule?.weight ?? fallback.weight,
      disabled: rule?.disabled ?? false,
      drain_fallback: rule?.drain_fallback ?? false,
      drain: rule?.drain ?? false,
      drain_until: rule?.drain_until ?? '',
      cache_domain: rule?.cache_domain ?? ''
    }
  })
  const members = new Set(view.members.map(member => member.account_id))
  const stale = policy.accounts.filter(rule => !members.has(rule.account_id))
  policy.accounts = []
  return { policy, rows, stale }
}

/** Restores a row to the policy defaults and drops its explicit rule. */
export function resetShareRow(draft: ShareDraft, row: ShareDraftRow): void {
  const fallback = shareDefaultRule(draft.policy, row)
  Object.assign(row, { configured: false, tier: fallback.tier, weight: fallback.weight, disabled: false, drain_fallback: false, drain: false, drain_until: '', cache_domain: '' })
}

/** Keeps unconfigured rows in step with a changed default weight mode. */
export function refreshShareDefaults(draft: ShareDraft): void {
  for (const row of draft.rows) {
    if (!row.configured) resetShareRow(draft, row)
  }
}

export function policyFromDraft(draft: ShareDraft): SharePolicy {
  const policy = cloneShareJSON(draft.policy)
  policy.accounts = draft.rows
    .filter(row => row.configured)
    .map(row => {
      const rule: ShareAccountRule = { account_id: row.account_id, tier: row.tier, weight: row.weight }
      if (row.disabled) rule.disabled = true
      if (row.drain && row.drain_fallback) rule.drain_fallback = true
      if (row.drain) rule.drain = true
      if (row.drain && row.drain_until) rule.drain_until = row.drain_until
      if (row.cache_domain.trim()) rule.cache_domain = row.cache_domain.trim()
      return rule
    })
    .sort((a, b) => a.account_id - b.account_id)
  return policy
}

export function draftFingerprint(draft: ShareDraft): string {
  return JSON.stringify(policyFromDraft(draft))
}

function inRange(value: number, min: number, max: number): boolean {
  return Number.isInteger(value) && value >= min && value <= max
}

/** Client-side checks matching the backend; the server still validates. */
export function validateShareDraft(draft: ShareDraft): ShareProblem[] {
  const problems: ShareProblem[] = []
  const domains = new Map<string, string>()
  for (const row of draft.rows) {
    if (!row.configured) continue
    const id = row.account_id
    if (!inRange(row.tier, TIER_MIN, TIER_MAX)) problems.push({ key: 'tierRange', params: { id } })
    if (!inRange(row.weight, SHARE_MIN_WEIGHT, SHARE_MAX_WEIGHT)) problems.push({ key: 'weightRange', params: { id, min: SHARE_MIN_WEIGHT, max: SHARE_MAX_WEIGHT } })
    if (row.drain_until && Number.isNaN(Date.parse(row.drain_until))) problems.push({ key: 'drainUntil', params: { id } })
    const domain = row.cache_domain.trim()
    if (!domain) continue
    if (domain.length > SHARE_MAX_CACHE_DOMAIN || !cacheDomainPattern.test(domain)) {
      problems.push({ key: 'cacheDomain', params: { id, max: SHARE_MAX_CACHE_DOMAIN } })
      continue
    }
    if (domain.startsWith(OWN_CACHE_DOMAIN_PREFIX)) {
      problems.push({ key: 'cacheDomainReserved', params: { id, prefix: OWN_CACHE_DOMAIN_PREFIX } })
      continue
    }
    const platform = domains.get(domain)
    if (platform === undefined) domains.set(domain, row.platform)
    else if (platform !== row.platform) problems.push({ key: 'cacheDomainPlatform', params: { domain } })
  }
  if (draft.policy.scheduler !== 'sub2api' && draft.rows.length > 0 && draft.rows.every(row => row.disabled || (row.drain && !row.drain_fallback)) && !draft.rows.every(row => row.disabled)) {
    problems.push({ key: 'allDrained' })
  }
  const { affinity, cache, tiers, balance } = draft.policy
  if (!inRange(affinity.ttl_seconds, 300, 86400)) problems.push({ key: 'affinityTTL' })
  if (!inRange(affinity.wait.ms_per_1k_tokens, 0, 5000)) problems.push({ key: 'waitPer1k' })
  if (!inRange(affinity.wait.max_ms, 0, 15000)) problems.push({ key: 'waitMax' })
  if (!inRange(affinity.wait.max_waiting, 0, 100)) problems.push({ key: 'maxWaiting' })
  if (!inRange(affinity.soft_spill_max_seconds, 0, 1800)) problems.push({ key: 'softSpill' })
  if (cache.ttl_seconds !== 0 && !inRange(cache.ttl_seconds, 60, 86400)) problems.push({ key: 'cacheTTL' })
  if (!inRange(tiers.stable_seconds, 0, 3600)) problems.push({ key: 'stableSeconds' })
  if (tiers.return_deadline_seconds !== 0 && !inRange(tiers.return_deadline_seconds, 600, 86400)) problems.push({ key: 'returnDeadline' })
  if (balance.unit === 'sessions' && balance.correction !== 'off') problems.push({ key: 'correctionSessions' })
  if (!inRange(balance.window_minutes, 5, 1440)) problems.push({ key: 'balanceWindow' })
  if (!inRange(balance.min_volume, 0, 1_000_000)) problems.push({ key: 'minVolume' })
  return problems
}

export interface ShareRowShares {
  target: number
  effective: number
  capacity: number
}

export interface ShareDraftShares {
  /** Null when no healthy open row can take new sessions. */
  activeTier: number | null
  byAccount: Map<number, ShareRowShares>
}

/**
 * Computes the draft's shares like the backend member view: target within the
 * tier among open (non-drained) rows, effective only in the active tier (the
 * lowest tier with a healthy open row) among its healthy rows.
 */
export function draftShares(draft: ShareDraft, healthy: (accountId: number) => boolean): ShareDraftShares {
  const tiers = new Map<number, { weight: number; healthyWeight: number; concurrency: number }>()
  for (const row of draft.rows) {
    if (row.drain || row.disabled) continue
    const totals = tiers.get(row.tier) ?? { weight: 0, healthyWeight: 0, concurrency: 0 }
    totals.weight += row.weight
    totals.concurrency += Math.max(row.concurrency, 0)
    if (healthy(row.account_id)) totals.healthyWeight += row.weight
    tiers.set(row.tier, totals)
  }
  let activeTier: number | null = null
  for (const [tier, totals] of tiers) {
    if (totals.healthyWeight > 0 && (activeTier === null || tier < activeTier)) activeTier = tier
  }
  const emergencyWeights = new Map<number, number>()
  if (activeTier === null) {
    for (const row of draft.rows) {
      if (row.disabled || !row.drain || !row.drain_fallback || !healthy(row.account_id) || (row.drain_until && Date.parse(row.drain_until) <= Date.now())) continue
      emergencyWeights.set(row.tier, (emergencyWeights.get(row.tier) ?? 0) + row.weight)
      if (activeTier === null || row.tier < activeTier) activeTier = row.tier
    }
  }
  const byAccount = new Map<number, ShareRowShares>()
  for (const row of draft.rows) {
    const totals = tiers.get(row.tier)
    const shares: ShareRowShares = { target: 0, effective: 0, capacity: 0 }
    if (!row.disabled && row.drain && row.drain_fallback && row.tier === activeTier && healthy(row.account_id)) {
      shares.effective = row.weight / (emergencyWeights.get(row.tier) || Infinity)
    }
    if (!row.drain && !row.disabled && totals) {
      if (totals.weight > 0) shares.target = row.weight / totals.weight
      if (totals.concurrency > 0) shares.capacity = Math.max(row.concurrency, 0) / totals.concurrency
      if (row.tier === activeTier && healthy(row.account_id) && totals.healthyWeight > 0) {
        shares.effective = row.weight / totals.healthyWeight
      }
    }
    byAccount.set(row.account_id, shares)
  }
  return { activeTier, byAccount }
}

interface ShareErrorLike {
  status?: number
  reason?: string
  code?: number | string
  metadata?: Record<string, unknown>
}

function shareError(err: unknown): ShareErrorLike {
  return err && typeof err === 'object' ? (err as ShareErrorLike) : {}
}

export function isShareConflict(err: unknown): boolean {
  const e = shareError(err)
  return e.status === 409 || e.reason === 'SHARE_SCHEDULING_VERSION_CONFLICT'
}

export function isShareUnavailable(err: unknown): boolean {
  const e = shareError(err)
  return e.status === 503 || e.reason === 'SHARE_SCHEDULING_UNAVAILABLE'
}

/** Lists the server's policy problems (one per line in metadata.problems). */
export function sharePolicyProblems(err: unknown): string[] {
  const e = shareError(err)
  if (e.reason !== 'SHARE_SCHEDULING_POLICY_INVALID') return []
  const raw = e.metadata?.problems
  return typeof raw === 'string' ? raw.split('\n').filter(Boolean) : []
}

export function formatSharePercent(value: number | null | undefined, digits = 1): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '-'
  return `${(value * 100).toFixed(digits)}%`
}

/** Converts between an RFC 3339 instant and a datetime-local input value. */
export function toLocalInput(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

export function fromLocalInput(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toISOString()
}

/** Formats milliseconds as "350 ms", "1.2 s" or "3.5 min". */
export function formatShareMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms)) return '-'
  if (ms < 1000) return `${Math.round(ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} min`
  return `${(ms / 3_600_000).toFixed(1)} h`
}
