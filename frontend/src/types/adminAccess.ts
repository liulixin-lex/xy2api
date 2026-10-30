/**
 * Operator role (普通管理员) access types.
 *
 * The wire shapes mirror `GET /api/v1/admin/me/access` and
 * `GET|PUT /api/v1/admin/users/:id/operator-permissions`.
 */
import type { CustomMenuItem } from '@/types'

export type AdminModuleId =
  | 'dashboard'
  | 'ops'
  | 'users'
  | 'groups'
  | 'channels'
  | 'accounts'
  | 'share_scheduling'
  | 'announcements'
  | 'support_tickets'
  | 'proxies'
  | 'security_audit'
  | 'redeem_codes'
  | 'promo_codes'
  | 'affiliates'
  | 'orders'
  | 'usage'
  | 'audit_logs'
  | 'subscriptions'

/** Module ids in the order of the approved module table (DEV_PLAN 4.2). */
export const ADMIN_MODULE_IDS: readonly AdminModuleId[] = [
  'dashboard',
  'ops',
  'users',
  'groups',
  'channels',
  'accounts',
  'share_scheduling',
  'announcements',
  'support_tickets',
  'proxies',
  'security_audit',
  'redeem_codes',
  'promo_codes',
  'affiliates',
  'orders',
  'usage',
  'audit_logs',
  'subscriptions'
] as const

export type AdminFlagId =
  | 'users.balance_adjust'
  | 'orders.refund'
  | 'redeem_codes.issue'
  | 'data.export'
  | 'accounts.upstream_address'

export const ADMIN_FLAG_IDS: readonly AdminFlagId[] = [
  'users.balance_adjust',
  'orders.refund',
  'redeem_codes.issue',
  'data.export',
  'accounts.upstream_address'
] as const

export type AdminLevel = 'view' | 'manage'

export type AdminPermission = `${AdminModuleId}:${AdminLevel}` | AdminFlagId

export interface AdminPermissionsWire {
  modules: Partial<Record<AdminModuleId, AdminLevel>>
  flags: AdminFlagId[]
}

export interface MeAccessSettings {
  ops_monitoring_enabled: boolean
  ops_realtime_monitoring_enabled: boolean
  ops_query_mode_default: string
  admin_usage_cache_hit_rate_enabled: boolean
  admin_usage_token_speed_enabled: boolean
  payment_enabled: boolean
  account_quota_notify_enabled: boolean
  web_search_emulation_enabled: boolean
  payment_subscription_usd_to_cny_rate: number
  payment_recharge_fee_rate: number
}

export interface MeAccess extends AdminPermissionsWire {
  role: 'admin' | 'operator'
  is_super_admin: boolean
  settings: MeAccessSettings
  custom_menu_items: CustomMenuItem[]
}
