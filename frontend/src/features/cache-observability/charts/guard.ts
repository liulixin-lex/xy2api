// The chart endpoints' contract as the panel enforces it (CACHE_PANEL_API §1, §1.1, §8; contract
// samples in 00-shared/cache_panel_contract v1.0.1). A response that carries identity data is refused
// whole; a value that would mislead is shown as "not computed" instead. Everything else renders as sent.
import type { BreakdownResponse, ExperimentsResponse, NorthStarResponse, TraceStatsResponse } from './types'

/** P-3: field names that must never appear, at any depth. */
const FORBIDDEN_KEYS = new Set([
  'session_digest',
  'request_id_hmac',
  'conv_anchor',
  'user_id',
  'api_key_id',
  'api_key',
  'email',
  'prompt_cache_key',
  'user_agent',
  'session_id',
  'ip_address',
  'is_fallback',
  'token',
])

/** P-3: values shaped like an HMAC or session digest, an email address or a key (same rules as F's self-check). */
const FORBIDDEN_VALUES = [/^v2:[0-9a-f]{16}$/, /^[0-9a-f]{32,}$/, /[^@\s]+@[^@\s]+\.[A-Za-z]+/, /^sk-/]

/** Ratios lie in [0, 1] by definition (§1); anything else cannot be trusted. */
const RATIO_KEYS = new Set([
  'token_hit_upper',
  'token_hit_lower',
  'request_hit',
  'business_coverage',
  'standardized_hit',
  'standardized_weight_coverage',
  'priced_p_share',
  'upstream_covered_p_share',
  'token_hit_cost_weighted_upper',
  'token_hit_cost_weighted_lower',
  'opportunity_share',
  'opportunity_amount_share',
  'coverage_ratio',
  'category_known_ratio',
  'pck_out_is_uuid_ratio',
  'in_ratio',
  'out_ratio',
  'model_isolated_switch_rate',
  'read_share',
  'token_hit',
  'under_read_share',
])

/** Thrown for a response the panel must not show; the section says it was withheld. Never carries the value. */
export class ContractRejection extends Error {
  readonly path: string

  constructor(path: string) {
    super(`cache panel response withheld: ${path}`)
    this.name = 'ContractRejection'
    this.path = path
  }
}

function scan(value: unknown, path: string): void {
  if (Array.isArray(value)) {
    value.forEach((item, index) => scan(item, `${path}[${index}]`))
  } else if (value && typeof value === 'object') {
    for (const [key, item] of Object.entries(value)) {
      if (FORBIDDEN_KEYS.has(key)) throw new ContractRejection(`${path}.${key}`)
      scan(item, `${path}.${key}`)
    }
  } else if (typeof value === 'string' && FORBIDDEN_VALUES.some((rule) => rule.test(value))) {
    throw new ContractRejection(path)
  }
}

/** A ratio outside [0, 1] is shown as not computed rather than as, say, 130 %. */
function dropBadRatios(value: unknown): void {
  if (Array.isArray(value)) {
    value.forEach(dropBadRatios)
  } else if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    for (const [key, item] of Object.entries(record)) {
      if (RATIO_KEYS.has(key) && typeof item === 'number' && !(item >= 0 && item <= 1)) record[key] = null
      else dropBadRatios(item)
    }
  }
}

function common<T>(data: T): T {
  scan(data, '$')
  dropBadRatios(data)
  return data
}

export function checkNorthStar(data: NorthStarResponse): NorthStarResponse {
  common(data)
  const summary = data.summary
  // §8.2: the cost key is always sent; a backend that leaves it out has no amounts to show.
  if (summary.cost === undefined) summary.cost = null
  // The upstream coverage share only means something in the upstream view.
  if (summary.cost && summary.cost.view !== 'upstream') summary.cost.upstream_covered_p_share = null
  return data
}

export function checkBreakdown(data: BreakdownResponse, limit: number): BreakdownResponse {
  common(data)
  // P-6: at most `limit` rows; meta is always an object; w_m only for a priced model (§8.3).
  data.rows = (data.rows ?? []).slice(0, limit)
  for (const row of data.rows) {
    const meta = row.meta && typeof row.meta === 'object' ? row.meta : {}
    if (meta.priced === false) delete meta.w_m
    row.meta = meta
  }
  return data
}

export function checkTraceStats(data: TraceStatsResponse): TraceStatsResponse {
  common(data)
  // P-7: n/a_* classes are coverage gaps and never part of the opportunity.
  for (const item of data.loss_classes ?? []) {
    if (item.class.startsWith('n/a_')) {
      item.excluded = true
      item.opportunity_tokens = null
    }
  }
  return data
}

export function checkExperiments(data: ExperimentsResponse): ExperimentsResponse {
  return common(data)
}
