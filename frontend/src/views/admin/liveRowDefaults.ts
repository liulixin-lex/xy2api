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
