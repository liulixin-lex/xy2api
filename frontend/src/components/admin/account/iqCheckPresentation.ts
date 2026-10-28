import type { IQAttempt, IQChallenge, IQDiagnostic, IQRunAccount, IQRunTarget, IQTargetSummary, IQVerdict } from '@/types'

type Translate = (key: string, params?: Record<string, string | number>) => string
export type IQDetailRow = { label: string; value: string; raw?: string }
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

const technicalKeys = [
  ['iqRequest', ['protocol', 'http_status', 'request_id', 'first_byte_ms', 'total_ms', 'media_type', 'content_encoding', 'transport', 'retry_visibility', 'error_code', 'error_type', 'retry_after', 'retry_after_unbounded']],
  ['iqParsing', ['code', 'category', 'limit_kind', 'limit_bytes', 'stage', 'answer_source', 'done_messages', 'terminal_items', 'ignored_items', 'ignored_types', 'event_type', 'event_index', 'bytes_read', 'field', 'offset', 'format_detected', 'parser_version']]
] as const

/** Known bounded diagnostic fields only; unknown keys are never rendered. */
export function iqTechnicalGroups(d: IQDiagnostic | null, t: Translate, exists: (key: string) => boolean) {
  const translated = (group: string, value: string) => exists(prefix + group + '.' + value) ? t(prefix + group + '.' + value) : value
  const row = (key: string, value: unknown): IQDetailRow[] => {
    if (value == null || value === '' || (key === 'code' && value === 'correct_answer')) return []
    const raw = String(value)
    let display = raw
    if (key === 'category') display = translated('iqCategories', raw)
    if (key === 'limit_kind') display = translated('iqLimits', raw)
    if (key === 'stage') display = translated('iqStages', raw)
    if (key === 'answer_source') display = translated('iqAnswerSources', raw)
    if (key === 'retry_visibility') display = translated('iqRetryVisibility', raw)
    if (key === 'code') display = translated('iqReasons', raw)
    if (key === 'protocol') display = ({ responses: 'Responses API', chat_completions: 'Chat Completions API' })[raw] ?? raw
    if (key === 'transport') display = raw === 'http' ? 'HTTP' : raw === 'plugin' ? t(prefix + 'iqPlugin') : raw
    if (key === 'media_type') display = ({ 'text/event-stream': 'SSE', 'application/json': 'JSON' })[raw] ?? raw
    if (key === 'first_byte_ms' || key === 'total_ms') display = iqSeconds(Number(value))
    if (key === 'format_detected' || key === 'retry_after_unbounded') display = t(prefix + (value ? 'iqYes' : 'iqNo'))
    if (key === 'bytes_read' || key === 'limit_bytes') display = `${Number(value).toLocaleString()} B`
    return [{ label: t(prefix + 'iqDiagnosticLabels.' + key), value: display, raw }]
  }
  return technicalKeys
    .map(([label, keys]) => ({ label: t(prefix + label), rows: keys.flatMap(key => row(key, d?.[key])) }))
    .filter(group => group.rows.length)
}
