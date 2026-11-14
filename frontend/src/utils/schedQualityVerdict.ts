import type { SchedEvidenceNumbers, SchedPolicy, SchedVerdict } from '@/types/schedQuality'

// The verdict table of contract section 5, for the group settings trial only: verdicts under saved thresholds
// always come from the API. MinSamples / MinSessions mirror backend schedquality/constants.go.
export const SCHED_MIN_SAMPLES = 30
export const SCHED_MIN_SESSIONS = 4

export type SchedTrialThresholds = Pick<SchedPolicy, 'ttft_seconds' | 'max_error_rate' | 'min_cache_ratio'>
export interface SchedTrialVerdicts {
  ttfo: SchedVerdict
  error: SchedVerdict
  cache: SchedVerdict
}

const tooFew = (n: number, sessions: number) => n < SCHED_MIN_SAMPLES || sessions < SCHED_MIN_SESSIONS

// Contract clarification K1: compare whole milliseconds; the API already rounds p90 and the fail bound up.
// A *_timeout flag means +∞.
export function ttfoVerdict(ttftSeconds: number, e: SchedEvidenceNumbers['ttfo']): SchedVerdict {
  if (tooFew(e.n, e.sessions) || (e.p90_ms === null && !e.p90_timeout)) return 'nodata'
  const limitMs = Math.round(ttftSeconds * 1000)
  if (e.fail_bound_timeout || (e.fail_bound_ms !== null && limitMs < e.fail_bound_ms)) return 'fail'
  if (e.p90_timeout || (e.p90_ms !== null && limitMs < e.p90_ms)) return 'marginal'
  return 'pass'
}

export function errorVerdict(maxErrorRate: number, e: SchedEvidenceNumbers['error']): SchedVerdict {
  if (tooFew(e.n, e.sessions) || e.rate === null) return 'nodata'
  if (e.lcb !== null && maxErrorRate < e.lcb) return 'fail'
  if (maxErrorRate < e.rate) return 'marginal'
  return 'pass'
}

export function cacheVerdict(minCacheRatio: number, e: SchedEvidenceNumbers['cache']): SchedVerdict {
  if (tooFew(e.n, e.sessions) || e.ratio === null) return 'nodata'
  return e.ratio < minCacheRatio ? 'fail' : 'pass'
}

export function trialVerdicts(thresholds: SchedTrialThresholds, evidence: SchedEvidenceNumbers): SchedTrialVerdicts {
  return {
    ttfo: ttfoVerdict(thresholds.ttft_seconds, evidence.ttfo),
    error: errorVerdict(thresholds.max_error_rate, evidence.error),
    cache: cacheVerdict(thresholds.min_cache_ratio, evidence.cache)
  }
}
