/**
 * Health verdict of a share group: the current window against the same
 * length window before the latest scheduler change. A pure function so the
 * thresholds stay testable in one place.
 */

import type { ShareMetrics } from '@/api/admin/shareScheduling'

export type ShareHealthLevel = 'ok' | 'watch' | 'rollback' | 'insufficient'

export type ShareHealthMetricKey = 'cacheHit' | 'switchLoss' | 'ttft' | 'errors'

export type ShareHealthPartKey = 'cacheHit' | 'switchLoss' | 'p50' | 'p95' | 'errors'

export interface ShareHealthPart {
  key: ShareHealthPartKey
  unit: 'ratio' | 'ms'
  current: number | null
  baseline: number | null
  level: ShareHealthLevel
}

export interface ShareHealthMetric {
  key: ShareHealthMetricKey
  level: ShareHealthLevel
  parts: ShareHealthPart[]
}

export interface ShareHealthVerdict {
  level: ShareHealthLevel
  /** Why there is no verdict: no baseline, too short a window since the switch, or too few requests. */
  insufficient?: 'baseline' | 'window' | 'requests'
  metrics: ShareHealthMetric[]
}

interface Band {
  watch: number
  /** Absent: the measure never calls for a rollback. */
  rollback?: number
}

/**
 * Initial thresholds, to be calibrated against production (待生产校准).
 * Sources: the UX audit §6.3 and the group 18 readiness review §0.3.
 * Ratios are fractions: 0.01 is one percentage point. A band without a
 * rollback level only ever asks for attention.
 *
 * Only cache lost to switching, the error rate and the slow first token
 * (P95) can call for a rollback. The overall cache hit ratio and the typical
 * first token move with the time of day and the model mix, and the baseline
 * is an earlier window, so they only ask for attention.
 */
export const SHARE_HEALTH_THRESHOLDS = {
  /** Minutes of data the current window needs since the switch. */
  minWindowMinutes: 30,
  /** Successful requests each window needs. */
  minRequests: 300,
  /** First-token percentiles need this many streamed samples in both windows. */
  minTTFTSamples: 50,
  /** Cache lost to switching needs this many attributed requests in both windows (about 1 in 4 sessions is sampled). */
  minAttributionSamples: 100,
  /** Cache hit ratio falling by this many points; attention only. */
  cacheHitDrop: { watch: 0.03 } satisfies Band,
  /** Cache lost to switching rising by this many points. */
  switchLossRise: { watch: 0.005, rollback: 0.02 } satisfies Band,
  /** Typical first token (P50) slower by this factor; attention only. */
  ttftP50Ratio: { watch: 1.15 } satisfies Band,
  /** Slow first token (P95) slower by this factor. */
  ttftP95Ratio: { watch: 1.2, rollback: 1.5 } satisfies Band,
  /** Error rate rising by this many points. */
  errorRise: { watch: 0.005, rollback: 0.02 } satisfies Band
} as const

const rank: Record<ShareHealthLevel, number> = { insufficient: 0, ok: 1, watch: 2, rollback: 3 }

function worst(levels: ShareHealthLevel[]): ShareHealthLevel {
  return levels.reduce<ShareHealthLevel>((acc, level) => (rank[level] > rank[acc] ? level : acc), 'insufficient')
}

// Ratios subtract in floating point (0.30 - 0.29 is 0.00999…), so a change
// reaching a threshold within this tolerance counts as reaching it.
const EPSILON = 1e-9

function band(change: number, limits: Band): ShareHealthLevel {
  if (limits.rollback !== undefined && change >= limits.rollback - EPSILON) return 'rollback'
  if (change >= limits.watch - EPSILON) return 'watch'
  return 'ok'
}

const known = (value: number | null | undefined): value is number => value !== null && value !== undefined && Number.isFinite(value)

function part(
  key: ShareHealthPartKey,
  unit: 'ratio' | 'ms',
  current: number | null,
  baseline: number | null,
  enough: boolean,
  judge: (now: number, before: number) => ShareHealthLevel
): ShareHealthPart {
  const level = enough && known(current) && known(baseline) ? judge(current, baseline) : 'insufficient'
  return { key, unit, current, baseline, level }
}

function metric(key: ShareHealthMetricKey, parts: ShareHealthPart[]): ShareHealthMetric {
  return { key, level: worst(parts.map(p => p.level)), parts }
}

/**
 * Judges the current window against the baseline. Each metric takes the
 * worst of its parts and the verdict the worst measured metric; parts
 * without enough data on either side stay "insufficient" and do not count.
 *
 * @param currentMinutes length of the current window; after a recent switch
 *   the server cuts it at the switch, so it can be shorter than asked for.
 */
export function judgeShareHealth(current: ShareMetrics, baseline: ShareMetrics | null, currentMinutes?: number): ShareHealthVerdict {
  const t = SHARE_HEALTH_THRESHOLDS
  const b = baseline
  const ttftEnough = b !== null && current.ttft_samples >= t.minTTFTSamples && b.ttft_samples >= t.minTTFTSamples
  const attributionEnough = b !== null && current.attribution_samples >= t.minAttributionSamples && b.attribution_samples >= t.minAttributionSamples
  const requestsEnough = b !== null && current.requests >= t.minRequests && b.requests >= t.minRequests

  const metrics: ShareHealthMetric[] = [
    metric('cacheHit', [part('cacheHit', 'ratio', current.cache_hit_ratio, b?.cache_hit_ratio ?? null, requestsEnough, (now, before) => band(before - now, t.cacheHitDrop))]),
    metric('switchLoss', [part('switchLoss', 'ratio', current.switch_loss_ratio, b?.switch_loss_ratio ?? null, attributionEnough, (now, before) => band(now - before, t.switchLossRise))]),
    metric('ttft', [
      part('p50', 'ms', current.ttft_p50_ms, b?.ttft_p50_ms ?? null, ttftEnough, (now, before) => (before > 0 ? band(now / before, t.ttftP50Ratio) : 'insufficient')),
      part('p95', 'ms', current.ttft_p95_ms, b?.ttft_p95_ms ?? null, ttftEnough, (now, before) => (before > 0 ? band(now / before, t.ttftP95Ratio) : 'insufficient'))
    ]),
    metric('errors', [part('errors', 'ratio', current.error_rate, b?.error_rate ?? null, requestsEnough, (now, before) => band(now - before, t.errorRise))])
  ]

  if (b === null) return { level: 'insufficient', insufficient: 'baseline', metrics }
  if (currentMinutes !== undefined && currentMinutes < t.minWindowMinutes) return { level: 'insufficient', insufficient: 'window', metrics }
  if (!requestsEnough) return { level: 'insufficient', insufficient: 'requests', metrics }
  return { level: worst(metrics.map(m => m.level)), metrics }
}

/**
 * Share of selections that moved a warm session on share scheduling's own
 * account (not a failover) above which the readiness review §0.3 advises
 * going back to the original scheduler.
 */
export const SHARE_HOT_MIGRATION_LIMIT = 0.001

/** Hot migrations against the selections they come from; null without selections. */
export function shareHotMigrationRatio(total: number, selections: number | null | undefined): number | null {
  return selections && selections > 0 ? total / selections : null
}
