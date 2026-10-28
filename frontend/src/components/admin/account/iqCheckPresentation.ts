import type { IQAttempt, IQChallenge, IQDiagnostic, IQRunAccount, IQRunTarget, IQTargetSummary, IQVerdict } from '@/types'

type Translate = (key: string, params?: Record<string, string | number>) => string
export type IQDetailRow = { key: string; label: string; value: string }
const prefix = 'admin.accounts.'

export const IQ_BUILTIN_MODELS = ['gpt-6-astra', 'gpt-6.1-sol'] as const
export const isIQBuiltinModel = (model: string) => (IQ_BUILTIN_MODELS as readonly string[]).includes(model)
export const iqModelLabel = (model: string) => ({ 'gpt-6-astra': 'Astra', 'gpt-6.1-sol': 'Sol' })[model] ?? model
export const iqEffortLabel = (value?: string) => value === 'upstream_default' ? 'default' : value || '—'
export const iqSeconds = (value: number) => (value / 1000).toFixed(2) + ' s'
/** formatDateTime options for the hh:mm recheck time. */
export const IQ_CLOCK: Intl.DateTimeFormatOptions = { hour: '2-digit', minute: '2-digit', hour12: false }

/** Built-in targets first, then custom targets in server order. */
export function sortIQTargets<T extends { model: string }>(targets: T[]): T[] {
  const rank = (model: string) => { const index = (IQ_BUILTIN_MODELS as readonly string[]).indexOf(model); return index < 0 ? IQ_BUILTIN_MODELS.length : index }
  return [...targets].sort((a, b) => rank(a.model) - rank(b.model))
}

/** Account-level summary used by the list filter: degraded if any enabled target is degraded, smart if every enabled target is smart. */
export function iqAccountVerdict(targets: IQTargetSummary[] | undefined): IQVerdict {
  const enabled = (targets ?? []).filter(target => target.enabled)
  if (enabled.some(target => target.verdict === 'degraded')) return 'degraded'
  return enabled.length && enabled.every(target => target.verdict === 'smart') ? 'smart' : 'unknown'
}

/** Counts accepted and rejected run requests; a rejected account counts once, whatever its targets. */
export function iqRunSummary(results: IQRunTarget[] | IQRunAccount[]) {
  const accounts = results.filter((item): item is IQRunAccount => 'account_id' in item)
  const targets = accounts.length ? accounts.flatMap(account => account.targets) : results as IQRunTarget[]
  const rejectedTargets = targets.filter(target => target.state === 'rejected')
  const rejectedAccounts = accounts.filter(account => account.reason)
  return {
    queued: targets.length - rejectedTargets.length,
    rejected: rejectedTargets.length + rejectedAccounts.length,
    reasons: [...new Set([...rejectedAccounts.map(account => account.reason!), ...rejectedTargets.map(target => target.reason ?? '')].filter(Boolean))]
  }
}

export const iqTargetBusy = (target: Pick<IQTargetSummary, 'state'>) => target.state === 'queued' || target.state === 'running'

// IQ gates are persisted model rate limits keyed "iq:<outbound model>". They are fail-closed: present means active,
// whatever the reset time says, so they are listed apart from ordinary rate limits.
export const IQ_GATE_KEY_PREFIX = 'iq:'
export type IQGateEntry = { model: string; reset_at: string }
type ModelRateLimits = Record<string, { rate_limit_reset_at: string }>
export function iqGateEntries(extra: Record<string, unknown> | undefined): IQGateEntry[] {
  const limits = extra?.model_rate_limits as ModelRateLimits | undefined
  return Object.entries(limits ?? {}).filter(([key]) => key.startsWith(IQ_GATE_KEY_PREFIX)).map(([key, info]) => ({ model: key.slice(IQ_GATE_KEY_PREFIX.length), reset_at: info.rate_limit_reset_at }))
}

export const IQ_FLAVORS = ['apple', 'peach', 'watermelon'] as const
export const IQ_SHAPES = ['circle', 'star'] as const

/** Rows and columns in the order the model saw them. */
export function iqChallengeTable(challenge: IQChallenge) {
  return {
    flavors: [...challenge.cols],
    rows: challenge.rows.map(shape => ({ shape, counts: challenge.cols.map(flavor => (shape === 0 ? challenge.r : challenge.s)[flavor]) }))
  }
}

export type IQAttemptOutcome = 'correct' | 'wrong' | 'naive' | 'downgrade' | 'conflict' | 'unknown'
export function iqAttemptOutcome(attempt: IQAttempt): IQAttemptOutcome {
  if (attempt.status === 'smart') return 'correct'
  if (attempt.status === 'degraded') {
    if (attempt.reason === 'model_mismatch') return 'downgrade'
    return attempt.challenge && attempt.answer === String(attempt.challenge.naive) ? 'naive' : 'wrong'
  }
  return attempt.reason === 'model_conflict' ? 'conflict' : 'unknown'
}

/** Downgrades are counted as wrong (red); conflicts and source disagreement are only flagged (amber). */
export function iqModelClassTone(attempt: IQAttempt): 'danger' | 'warning' | 'neutral' {
  if (attempt.model_class === 'downgrade') return 'danger'
  const flags = attempt.diagnostic?.model_identity?.flags ?? []
  return attempt.model_class === 'event_conflict' || flags.includes('source_disagreement') ? 'warning' : 'neutral'
}

const numberOf = (d: IQDiagnostic | null, key: string) => typeof d?.[key] === 'number' ? d[key] as number : undefined
export function iqTokenSummary(d: IQDiagnostic | null, t: Translate) {
  const input = numberOf(d, 'input_tokens'), output = numberOf(d, 'output_tokens'), reasoning = numberOf(d, 'reasoning_tokens')
  const parts: string[] = []
  if (input != null) parts.push(t(prefix + 'iqInputTokensValue', { value: input }))
  if (output != null) parts.push(t(prefix + 'iqOutputTokensValue', { value: output }) + (reasoning != null ? t(prefix + 'iqReasoningIncluded', { value: reasoning }) : ''))
  else if (reasoning != null) parts.push(t(prefix + 'iqReasoningTokensValue', { value: reasoning }))
  return parts.join(' · ')
}

const stringOf = (d: IQDiagnostic | null, key: string) => typeof d?.[key] === 'string' ? d[key] as string : ''

/**
 * Technical detail rows: request ID, timing, tokens, and failure facts. Reads the compact diagnostic stored for correct
 * answers and the full one kept for wrong or unknown answers alike; raw codes are translated, never shown beside.
 */
export function iqTechnicalRows(attempt: IQAttempt, t: Translate, exists: (key: string) => boolean, formatTime: (value: string) => string): IQDetailRow[] {
  const d = attempt.diagnostic
  const translated = (group: string, value: string) => exists(prefix + group + '.' + value) ? t(prefix + group + '.' + value) : value
  const firstByte = numberOf(d, 'first_byte_ms'), total = numberOf(d, 'total_ms'), status = numberOf(d, 'http_status')
  const retryAfter = stringOf(d, 'retry_after')
  // Upstream error code and type come from one closed backend list; only the common ones are worded, the rest stay hidden.
  const errorKeys = ['error_code', 'error_type'].map(key => stringOf(d, key)).filter(Boolean).map(value => prefix + 'iqUpstreamErrors.' + value).filter(exists)
  const upstreamError = [...new Set(errorKeys.map(key => t(key)))].join(' · ')
  const stage = stringOf(d, 'stage'), reason = stringOf(d, 'code') || attempt.reason
  const failure = attempt.status === 'unknown' ? [stage && translated('iqStages', stage), reason && translated('iqReasons', reason)].filter(Boolean).join(' · ') : ''
  const rows: [string, string][] = [
    ['request_id', stringOf(d, 'request_id')],
    ['latency', [firstByte ? t(prefix + 'iqFirstByteValue', { value: iqSeconds(firstByte) }) : '', total ? t(prefix + 'iqTotalValue', { value: iqSeconds(total) }) : ''].filter(Boolean).join(' / ')],
    ['tokens', iqTokenSummary(d, t)],
    ['http_status', status != null && status !== 200 ? String(status) : ''],
    ['error', upstreamError],
    ['retry_after', retryAfter ? formatTime(retryAfter) : d?.retry_after_unbounded === true ? t(prefix + 'iqRetryAfterUnbounded') : ''],
    ['failure', failure]
  ]
  return rows.filter(([, value]) => value).map(([key, value]) => ({ key, label: t(prefix + 'iqDiagnosticLabels.' + key), value }))
}
