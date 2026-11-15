/**
 * Defaults for the live engine's partial sources (useLiveTable): a partial source must answer every requested id with
 * every field it owns, empty values given explicitly, because a field it leaves out is never cleared. The backend
 * leaves empty values out, so these fill them in.
 */
import type { BatchUserUsageStats } from '@/api/admin/dashboard'

/** A user's usage with zero costs and no platforms when the batch answer has nothing (or only part) for the user. */
export function userUsageOrDefault(userId: number, stats: Partial<BatchUserUsageStats> | undefined): BatchUserUsageStats {
  return {
    user_id: userId,
    today_actual_cost: stats?.today_actual_cost ?? 0,
    total_actual_cost: stats?.total_actual_cost ?? 0,
    by_platform: stats?.by_platform ?? []
  }
}

export interface GroupUsageSummary {
  today_cost: number
  yesterday_cost: number
  total_cost: number
}

/** A group's costs with zeros for whatever the usage summary leaves out (groups with no usage are not listed). */
export function groupUsageOrDefault(item: Partial<GroupUsageSummary> | undefined): GroupUsageSummary {
  return {
    today_cost: item?.today_cost ?? 0,
    yesterday_cost: item?.yesterday_cost ?? 0,
    total_cost: item?.total_cost ?? 0
  }
}

type AccountCounts = { account_count?: number; active_account_count?: number; rate_limited_account_count?: number }

/** The group list omits a count once it is 0: give all three explicitly. */
export function withAccountCounts<G extends AccountCounts>(group: G): G {
  return {
    ...group,
    account_count: group.account_count ?? 0,
    active_account_count: group.active_account_count ?? 0,
    rate_limited_account_count: group.rate_limited_account_count ?? 0
  }
}
