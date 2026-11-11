import { extractI18nErrorMessage } from '@/utils/apiError'
import { isStepUpBlocked, stepUpBlockReason } from '@/composables/useStepUp'
import type { Campaign, CampaignStatus } from './types'

type Translate = (key: string, params?: Record<string, unknown>) => string

/** Localized message for a failed campaign request, by reason code when known. */
export function campaignErrorMessage(err: unknown, t: Translate): string {
  if (isStepUpBlocked(err)) {
    return stepUpBlockReason(err) === 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
      ? t('stepUp.adminApiKeyForbidden')
      : t('stepUp.notEnabled')
  }
  return extractI18nErrorMessage(err, t, 'admin.emailCampaigns.errors', t('admin.emailCampaigns.errors.generic'))
}

/** Whether a request rejection is an abort of a superseded request. */
export function isAbortError(err: unknown): boolean {
  if (!err || typeof err !== 'object') return false
  const e = err as { name?: string; code?: string }
  return e.name === 'AbortError' || e.name === 'CanceledError' || e.code === 'ERR_CANCELED'
}

/** Split a textarea into trimmed, non-empty, de-duplicated lines. */
export function parseLines(text: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim()
    if (!line) continue
    const key = line.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(line)
  }
  return out
}

/** A campaign that blocks creating another one. */
export function isActiveStatus(status: CampaignStatus): boolean {
  return status === 'sending' || status === 'paused'
}

/**
 * Rough sending time in minutes for `count` mails at the given limits:
 * each 24-hour window sends up to `perDay` mails at `perMinute` per minute.
 */
export function estimateMinutes(count: number, perMinute: number, perDay: number): number {
  if (count <= 0 || perMinute <= 0 || perDay <= 0) return 0
  const fullDays = Math.floor((count - 1) / perDay)
  const rest = count - fullDays * perDay
  return fullDays * 24 * 60 + Math.ceil(rest / perMinute)
}

/** "约 3 小时 20 分钟" style duration. */
export function formatDuration(minutes: number, t: Translate): string {
  if (minutes <= 1) return t('admin.emailCampaigns.duration.underMinute')
  if (minutes < 60) return t('admin.emailCampaigns.duration.minutes', { n: minutes })
  if (minutes < 24 * 60) {
    const hours = Math.floor(minutes / 60)
    const rest = minutes % 60
    return rest > 0
      ? t('admin.emailCampaigns.duration.hoursMinutes', { h: hours, m: rest })
      : t('admin.emailCampaigns.duration.hours', { h: hours })
  }
  const days = Math.floor(minutes / (24 * 60))
  const hours = Math.round((minutes % (24 * 60)) / 60)
  return hours > 0
    ? t('admin.emailCampaigns.duration.daysHours', { d: days, h: hours })
    : t('admin.emailCampaigns.duration.days', { d: days })
}

/** Thousands separators, e.g. 1,234. */
export function formatCount(n: number | null | undefined): string {
  return Number(n ?? 0).toLocaleString('en-US')
}

/** Recipients that are processed (no longer pending or in flight). */
export function processedCount(c: Campaign): number {
  return c.counts.sent + c.counts.failed + c.counts.skipped
}

export function progressPercent(c: Campaign): number {
  if (!c.counts.total) return 0
  return Math.min(100, Math.round((processedCount(c) / c.counts.total) * 100))
}

export function statusBadgeClass(status: CampaignStatus): string {
  switch (status) {
    case 'sending':
      return 'badge-primary'
    case 'paused':
      return 'badge-warning'
    case 'completed':
      return 'badge-success'
    default:
      return 'badge-gray'
  }
}
