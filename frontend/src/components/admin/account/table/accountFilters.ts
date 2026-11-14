/**
 * Whether an account on the page still matches the list's filters, judged in the browser from the row's own data. The
 * live table dims a row that no longer matches; the server's answer decides the page, this only mirrors it.
 */
import { iqAccountVerdict } from '@/components/admin/account/iqCheckPresentation'
import type { Account } from '@/types'
import type { AccountQualityView } from './accountGroups'

export interface AccountListFilters {
  platform?: string
  type?: string
  status?: string
  group?: string
  group_category?: string
  privacy_mode?: string
  iq_status?: string
  sched_quality?: string
  search?: string
}

/** Quality filter values, as the backend takes them. */
export type QualityFilter = 'normal' | 'substandard' | 'exempt' | 'observing'
export const QUALITY_FILTERS: QualityFilter[] = ['normal', 'substandard', 'exempt', 'observing']

export const UNGROUPED_GROUP_FILTER = 'ungrouped'
export const PRIVACY_UNSET_FILTER = '__unset__'

/**
 * The quality filter against the account's summary, as the backend's filter decides it (sched_admin_summary.go,
 * attention): only OpenAI accounts take part in any of the four; observing by the summary status, exempt by any
 * exemption, back of queue by a substandard record in effect while the account is not stopped, normal by status
 * normal (not stopped, not observed) with no exemption and no record in effect. An account the summary answered
 * without (`null`) matches none. Before the first summary of the row is in (`undefined`) nothing is judged: it matches,
 * so the rows of a fresh page do not dim for the moment the summary takes.
 */
export function qualityMatches(platform: string, quality: AccountQualityView | null | undefined, filter: string): boolean {
  if (!filter) return true
  if (platform !== 'openai') return false
  if (quality === undefined) return true
  if (quality === null) return false
  const back = !quality.stop && quality.substandard.some((item) => item.effective)
  switch (filter) {
    case 'observing':
      return quality.status === 'observing'
    case 'exempt':
      return quality.exemptions.length > 0
    case 'substandard':
      return back
    case 'normal':
      return quality.status === 'normal' && quality.exemptions.length === 0 && !back
    default:
      return true
  }
}

/** The clock is read only for a time the row has, so a row without one does not follow the page clock. */
const future = (value: string | null | undefined, now: () => number) => {
  const time = value ? new Date(value).getTime() : Number.NaN
  return Number.isFinite(time) && time > now()
}

export interface AccountMatchContext {
  /** The page clock (useLiveClock): a rate limit or a pause that ends dims the row at that second. */
  now: () => number
  /** The group-category filter (from the group categories store). */
  matchesCategory: (groupIds: number[], category: string) => boolean
  /** The row's quality summary: undefined until the first one is in, null when it answered without the account. */
  quality?: AccountQualityView | null
}

export function accountMatchesFilters(account: Account, filters: AccountListFilters, context: AccountMatchContext): boolean {
  if (filters.platform && account.platform !== filters.platform) return false
  if (filters.type && account.type !== filters.type) return false
  if (filters.iq_status && (account.platform !== 'openai' || iqAccountVerdict(account.iq_targets) !== filters.iq_status)) return false
  if (filters.status) {
    const rateLimited = future(account.rate_limit_reset_at, context.now)
    const tempUnschedulable = future(account.temp_unschedulable_until, context.now)
    if (filters.status === 'active') {
      if (account.status !== 'active' || rateLimited || tempUnschedulable || !account.schedulable) return false
    } else if (filters.status === 'rate_limited') {
      if (account.status !== 'active' || !rateLimited || tempUnschedulable) return false
    } else if (filters.status === 'temp_unschedulable') {
      if (account.status !== 'active' || !tempUnschedulable) return false
    } else if (filters.status === 'unschedulable') {
      if (account.status !== 'active' || account.schedulable || rateLimited || tempUnschedulable) return false
    } else if (account.status !== filters.status) {
      return false
    }
  }
  // group_ids is the row's only group list (the backend omits it when empty; merges write [] then).
  const groupIds = account.group_ids ?? []
  if (!context.matchesCategory(groupIds, filters.group_category ?? '')) return false
  if (filters.group) {
    if (filters.group === UNGROUPED_GROUP_FILTER) {
      if (groupIds.length > 0) return false
    } else if (!groupIds.includes(Number(filters.group))) {
      return false
    }
  }
  if (filters.privacy_mode) {
    const privacyMode = typeof account.extra?.privacy_mode === 'string' ? account.extra.privacy_mode : ''
    if (filters.privacy_mode === PRIVACY_UNSET_FILTER) {
      if (privacyMode.trim() !== '') return false
    } else if (privacyMode !== filters.privacy_mode) {
      return false
    }
  }
  const search = String(filters.search || '').trim().toLowerCase()
  if (search && !account.name.toLowerCase().includes(search)) return false
  return qualityMatches(account.platform, context.quality, filters.sched_quality ?? '')
}
