/**
 * A full account (the answer of an edit or a row action) written into the accounts table's row.
 *
 * - The backend leaves a list field out when it is empty (omitempty in dto.AccountListItem): an account removed from
 *   its last group comes back without group_ids, a cleared limit without its field. A partial merge would keep the old
 *   value on screen, so every such field the answer leaves out is written as empty; group_ids as [].
 * - The full structure's groups and account_groups are not list fields: they stay out of the row (the groups column
 *   reads group_ids and the groups store).
 */
import type { Account } from '@/types'

/** List fields the backend omits when empty (backend/internal/handler/dto/types.go, AccountListItem). */
export const LIST_FIELDS_OMITTED_WHEN_EMPTY = [
  'credentials', 'credentials_status', 'extra', 'ollama_cloud_usage', 'codex_turn_tickets', 'opencode_go_usage',
  'proxy_fallback_origin_name', 'load_factor', 'window_cost_limit', 'window_cost_sticky_reserve', 'max_sessions',
  'session_idle_timeout_minutes', 'base_rpm', 'rpm_strategy', 'rpm_sticky_buffer', 'user_msg_queue_mode',
  'enable_tls_fingerprint', 'tls_fingerprint_profile_id', 'session_id_masking_enabled', 'cache_ttl_override_enabled',
  'cache_ttl_override_target', 'custom_base_url_enabled', 'custom_base_url', 'quota_limit', 'quota_used',
  'quota_daily_limit', 'quota_daily_used', 'quota_weekly_limit', 'quota_weekly_used', 'quota_daily_reset_mode',
  'quota_daily_reset_hour', 'quota_weekly_reset_mode', 'quota_weekly_reset_day', 'quota_weekly_reset_hour',
  'quota_reset_timezone', 'quota_daily_reset_at', 'quota_weekly_reset_at', 'quota_notify_daily_enabled',
  'quota_notify_daily_threshold', 'quota_notify_weekly_enabled', 'quota_notify_weekly_threshold',
  'quota_notify_total_enabled', 'quota_notify_total_threshold', 'parent_account_id', 'quota_dimension', 'parent_email',
  'parent_plan_type', 'parent_privacy_mode', 'parent_subscription_expires_at', 'parent_chatgpt_account_id', 'proxy'
] as const

/** Fields of the full account that are not list fields. */
const FULL_ONLY_FIELDS = ['groups', 'account_groups'] as const

export function accountRowPatch<T extends object = Account>(account: Account): Partial<T> {
  const source = account as unknown as Record<string, unknown>
  const patch: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(source)) {
    if (!(FULL_ONLY_FIELDS as readonly string[]).includes(key)) patch[key] = value
  }
  for (const key of LIST_FIELDS_OMITTED_WHEN_EMPTY) if (!(key in source)) patch[key] = undefined
  patch.group_ids = Array.isArray(source.group_ids) ? source.group_ids : []
  return patch as Partial<T>
}
