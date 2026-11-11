/**
 * Email campaign (marketing mail) admin types.
 * Shapes follow the shared REST contract for `/api/v1/admin/email-campaigns`.
 */
import type { PaginatedResponse } from '@/types'

export type CampaignStatus = 'sending' | 'paused' | 'completed' | 'canceled'
export type PauseReason = '' | 'manual' | 'failures' | 'auth'
export type WaitingForLimit = '' | 'minute' | 'day'
export type RecipientStatus = 'pending' | 'sending' | 'sent' | 'failed' | 'skipped'
export type SuppressionSource = 'admin' | 'unsubscribe'

/** Audience conditions sent to /preview and create. */
export interface AudienceInput {
  /** "YYYY-MM-DD", inclusive, in `timezone`. */
  registered_from?: string | null
  /** "YYYY-MM-DD", inclusive. */
  registered_to?: string | null
  /** IANA time zone name. */
  timezone: string
  /** 1..3650 */
  inactive_days?: number | null
  /** Up to 1000 raw lines: numeric = user id, contains "@" = email. */
  exclude?: string[]
}

export interface SkipCounts {
  no_valid_email: number
  suppressed: number
  excluded: number
  duplicate: number
}

export interface CampaignAudience {
  registered_from: string | null
  registered_to: string | null
  timezone: string
  inactive_days: number | null
  exclude_user_ids: number[]
  skipped: SkipCounts
}

export interface CampaignCounts {
  total: number
  pending: number
  sending: number
  sent: number
  failed: number
  skipped: number
  /** Subset of `failed` that may have been delivered. */
  uncertain: number
}

export interface UserRef {
  id: number
  email: string
}

export interface Campaign {
  id: number
  subject: string
  status: CampaignStatus
  pause_reason: PauseReason
  last_error: string
  consecutive_failures: number
  waiting_for_limit: WaitingForLimit
  audience: CampaignAudience
  counts: CampaignCounts
  created_by: UserRef | null
  created_at: string
  started_at: string
  finished_at: string | null
  updated_at: string
}

export interface CampaignDetail extends Campaign {
  body_markdown: string
  /** Server-rendered HTML. Only ever shown in a sandboxed iframe. */
  preview_html: string
}

export interface Recipient {
  user_id: number
  email: string
  status: RecipientStatus
  last_error: string
  uncertain: boolean
  claimed_at: string | null
  sent_at: string | null
}

export interface Suppression {
  id: number
  email: string
  user_id: number | null
  username: string
  source: SuppressionSource
  note: string
  campaign_id: number | null
  campaign_subject: string
  created_by: UserRef | null
  created_at: string
}

export interface SendLimits {
  per_minute: number
  per_day: number
}

export interface CampaignSettings extends SendLimits {
  smtp_configured: boolean
  site_url: string
  site_url_ok: boolean
  sent_last_minute: number
  sent_last_24h: number
}

export interface PreviewRequest {
  audience: AudienceInput
  page?: number
  page_size?: number
}

export interface PreviewUser {
  user_id: number
  email: string
  username: string
  created_at: string
  last_activity_at: string | null
}

export interface PreviewResult extends PaginatedResponse<PreviewUser> {
  matched: number
  skipped: SkipCounts
  /** Number of users matched by the exclusion lines. */
  exclude_resolved: number
  exclude_not_found: string[]
}

export interface ContentInput {
  subject: string
  body_markdown: string
}

export interface RenderResult {
  subject: string
  html: string
  text: string
}

export interface TestSendInput extends ContentInput {
  to: string
}

export interface CreateCampaignInput extends ContentInput {
  audience: AudienceInput
}

export interface RetryFailedResult {
  campaign: Campaign
  requeued: number
}

export interface CampaignListQuery {
  status?: CampaignStatus
  page?: number
  page_size?: number
}

export interface RecipientListQuery {
  status?: RecipientStatus
  search?: string
  page?: number
  page_size?: number
}

export interface SuppressionListQuery {
  search?: string
  page?: number
  page_size?: number
}

export interface AddSuppressionsInput {
  entries: string[]
  note?: string
}

export interface AddSuppressionsResult {
  added: number
  already: number
  not_found: string[]
}
