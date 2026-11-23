// Shared by the usage table's cells, hover tips and request detail, so a value reads the same everywhere.
import { reasoningEffortValuesEqual } from '@/utils/format'
import { resolveUsageRequestType } from '@/utils/usageRequestType'
import type { AdminUsageLog } from '@/types'

type Translate = (key: string, params?: Record<string, unknown>) => string

/** The account-billed cost: (account_stats_cost ?? total_cost) × account rate. */
export function accountBilled(row: {
  total_cost?: number | null
  account_stats_cost?: number | null
  account_rate_multiplier?: number | null
}): number {
  const base = row.account_stats_cost != null ? row.account_stats_cost : (row.total_cost ?? 0)
  const result = base * (row.account_rate_multiplier ?? 1)
  return Number.isNaN(result) ? 0 : result
}

/** A latency: "800ms", "51.00s", past a minute "1m 10s", past an hour "1h 2m"; no value is "—". */
export function formatLatency(ms: number | null | undefined): string {
  if (ms == null) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  const totalSec = Math.round(ms / 1000)
  if (totalSec < 3600) return `${Math.floor(totalSec / 60)}m ${totalSec % 60}s`
  return `${Math.floor(totalSec / 3600)}h ${Math.floor((totalSec % 3600) / 60)}m`
}

/** A request without a bill (admin list only, mostly failed or canceled): no cost, often no token counts. */
export const isUnbilled = (row: AdminUsageLog) => row.source === 'unbilled'

/** The result word of a request without a bill: failed, canceled or ended locally, else none. */
export function unbilledWord(row: AdminUsageLog, t: Translate): string {
  if (row.result === 3) return t('admin.usage.resultFailed')
  if (row.result === 4) return t('admin.usage.resultCanceled')
  if (row.result === 5) return t('admin.usage.resultLocalEnded')
  if (row.result === 6) return t('admin.usage.resultLocalUndeliverable')
  return ''
}

/** The requested model and where it was sent: the mapping chain's steps, or requested then upstream when they differ. */
export function modelSteps(row: AdminUsageLog): string[] {
  if (row.model_mapping_chain && row.model_mapping_chain.includes('→')) return row.model_mapping_chain.split('→')
  if (row.upstream_model && row.upstream_model !== row.model) return [row.model, row.upstream_model]
  return [row.model]
}

const sentUpstreamModel = (row: AdminUsageLog): string => row.upstream_model?.trim() || row.model?.trim() || ''

const normalizeModelVariant = (model: string): string => model
  .trim()
  .toLowerCase()
  .replace(/-latest$/, '')
  .replace(/-\d{4}-\d{2}-\d{2}$/, '')
  .replace(/-\d{8}$/, '')

/** The upstream answered with another model than the one sent: shown with what kind of difference it is. */
export const hasModelAudit = (row: AdminUsageLog) => row.upstream_model_mismatch === true && !!row.upstream_response_model

/** The upstream's answer differs from the model sent only by a date or "-latest" suffix. */
export function isLikelyModelVariant(row: AdminUsageLog): boolean {
  const sent = sentUpstreamModel(row)
  const response = row.upstream_response_model?.trim() || ''
  return sent !== '' && response !== '' && normalizeModelVariant(sent) === normalizeModelVariant(response)
}

export function modelAuditTitle(row: AdminUsageLog, t: Translate): string {
  return [
    `${t('usage.requestedModel')}: ${row.model || '—'}`,
    `${t('usage.sentUpstreamModel')}: ${sentUpstreamModel(row) || '—'}`,
    `${t('usage.upstreamResponseModel')}: ${row.upstream_response_model || '—'}`,
  ].join('\n')
}

/** Requested and forwarded reasoning effort both known and different. */
export function hasReasoningEffortMapping(row: AdminUsageLog): boolean {
  const requested = row.reasoning_effort?.trim() || ''
  const forwarded = row.upstream_reasoning_effort?.trim() || ''
  return requested !== '' && forwarded !== '' && !reasoningEffortValuesEqual(requested, forwarded)
}

/** The group a request is listed under: on the admin page the group it asked for (before a fallback), else its group. */
export function groupLabel(row: AdminUsageLog, showFallbackOrigin: boolean): string {
  if (showFallbackOrigin && row.origin_group_id) return row.origin_group_name || `#${row.origin_group_id}`
  return row.group?.name || ''
}

/**
 * Served by another group than the request's (admin page only): judged by the original and the serving group ids, both
 * known and different (the bill's fallback flag means a rear account took the request, not another group).
 */
export function crossGroup(row: AdminUsageLog, showFallbackOrigin: boolean): boolean {
  return showFallbackOrigin && row.origin_group_id != null && row.group_id != null && row.origin_group_id !== row.group_id
}

export function requestTypeLabel(row: AdminUsageLog, t: Translate): string {
  const requestType = resolveUsageRequestType(row)
  if (requestType === 'cyber') return t('usage.cyber')
  if (requestType === 'live') return t('usage.live')
  if (requestType === 'ws_v2') return t('usage.ws')
  if (requestType === 'stream') return t('usage.stream')
  if (requestType === 'sync') return t('usage.sync')
  return t('usage.unknown')
}

/** The endpoint the request came in on, and the one it was sent to when the page shows it. */
export function endpointPair(row: AdminUsageLog, showUpstreamEndpoint: boolean): { inbound: string; upstream: string | null } {
  return {
    inbound: row.inbound_endpoint?.trim() || '',
    upstream: showUpstreamEndpoint ? row.upstream_endpoint?.trim() || '' : null,
  }
}

/** A token total in the stats: "652.44M", "12.30K", below a thousand the count itself. */
export function formatTokenTotal(value: number): string {
  if (value >= 1e9) return (value / 1e9).toFixed(2) + 'B'
  if (value >= 1e6) return (value / 1e6).toFixed(2) + 'M'
  if (value >= 1e3) return (value / 1e3).toFixed(2) + 'K'
  return value.toLocaleString()
}
