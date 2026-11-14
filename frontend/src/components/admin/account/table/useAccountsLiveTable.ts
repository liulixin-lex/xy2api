/**
 * The accounts page on the live table engine (spec 8.1, 8.5).
 *
 * - Page: the account list (lite=1) with the page's filters and server sort.
 * - Rows of the page, every 3 s: the same list with `ids` (whole rows; page 1, page size = the id count). The group
 *   filter goes along, as it decides the group concurrency the rows carry.
 * - Quality summary, every 5 s: the backend's own shape, a value (or null) for every id. qualityLanded() waits for
 *   the next one (a recovery holds its lock until the rows show the summary after it, or that summary fails).
 * - Today's stats and upstream balances, every 30 s: batch endpoints with defaults for every id.
 * - Usage windows: after each load only, from a 5-minute cache (a manual refresh skips the cache).
 * - Membership check: every 30 s (the engine's default); rows that no longer match the filters are dimmed in place.
 */
import { onScopeDispose } from 'vue'
import { adminAPI } from '@/api/admin'
import { backendSummary } from '@/api/admin/schedQuality'
import type { BackendAccountSummary } from '@/api/admin/schedQualityTypes'
import type { UpstreamBalanceItem } from '@/api/admin/accounts'
import { useLiveTable, type LiveRowId, type LiveTable, type LiveTask } from '@/composables/useLiveTable'
import type { AccountListItem, AccountUsageInfo, WindowStats } from '@/types'
import type { AccountListFilters } from './accountFilters'

export type LiveAccount = AccountListItem & {
  /** The account's quality summary as the backend sends it; null when it has none. */
  quality?: BackendAccountSummary | null
  today_stats?: WindowStats
  upstream_balance?: UpstreamBalanceItem | null
  usage_info?: AccountUsageInfo | null
  usage_error?: string | null
}

export interface AccountsLiveOptions {
  page: () => { page: number; pageSize: number }
  filters: () => AccountListFilters
  sort: () => { sort_by: string; sort_order: 'asc' | 'desc' }
  /** Whether a column that shows the data is visible (or, for quality, another use needs it). */
  wants: { quality: () => boolean; today: () => boolean; balance: () => boolean; usage: () => boolean }
  matches: (row: LiveAccount) => boolean
  /** The summary's page-wide flags (quality scheduling switched off, a stale view). */
  onQualityMeta?: (meta: { global_enabled: boolean; stale: boolean }) => void
  /** A page answer arrived for these page and size (the pagination bar follows it, not the click). */
  onPageShown?: (page: { page: number; pageSize: number }) => void
  /** The page's own tasks (the quality panel and board, useSchedLayerData). */
  tasks?: LiveTask[]
}

export const ROW_BEAT_MS = 3000
export const QUALITY_BEAT_MS = 5000
export const STATS_BEAT_MS = 30_000
export const USAGE_CACHE_MS = 5 * 60_000
const QUALITY_MAX_IDS = 1000
const BALANCE_MAX_IDS = 200

export const emptyTodayStats = (): WindowStats => ({ requests: 0, tokens: 0, cost: 0, standard_cost: 0, user_cost: 0 })

/** The platforms whose usage windows the batch endpoint answers. */
export function supportsBatchUsage(account: Pick<AccountListItem, 'platform' | 'type'>): boolean {
  if (account.platform === 'anthropic') return account.type === 'oauth' || account.type === 'setup-token'
  if (account.platform === 'gemini') return true
  if (['antigravity', 'openai', 'grok'].includes(account.platform)) return account.type === 'oauth'
  return false
}

const chunk = <T>(items: T[], size: number) => Array.from({ length: Math.ceil(items.length / size) }, (_, i) => items.slice(i * size, (i + 1) * size))
const numericIds = (ids: LiveRowId[]) => ids.map((id) => Number(id))

export function useAccountsLiveTable(options: AccountsLiveOptions) {
  /** The server's page count for the last page answer (the page clamp reads it). */
  let pages = 0
  let balanceForbidden = false
  let forceUsage = false
  /** Quality runs started so far, and the callers waiting for one started after a given run to be written. */
  let qualityRuns = 0
  const qualityWaiters: Array<{ after: number; resolve: () => void }> = []
  /** Lets go of the callers waiting for a run started up to `run`. */
  const releaseQuality = (run: number) => {
    for (const waiter of [...qualityWaiters]) {
      if (waiter.after < run) {
        qualityWaiters.splice(qualityWaiters.indexOf(waiter), 1)
        waiter.resolve()
      }
    }
  }
  // The engine writes the answer once fetch returns; the waiters hear of it after that.
  const settleQuality = (run: number) => setTimeout(() => releaseQuality(run), 0)
  // Leaving the page: nobody is left to wait for (7g-2).
  onScopeDispose(() => releaseQuality(Number.POSITIVE_INFINITY))
  const usageCache = new Map<number, { usage: AccountUsageInfo | null; error: string | null; at: number }>()

  const table: LiveTable<LiveAccount> = useLiveTable<LiveAccount>({
    getId: (row) => row.id,
    fetchPage: async (signal) => {
      const { page, pageSize } = options.page()
      const result = await adminAPI.accounts.list(page, pageSize, { ...options.filters(), ...options.sort(), lite: '1' }, { signal })
      pages = result.pages ?? 0
      // A newer load aborts this one; only the answer that is shown moves the pagination bar.
      if (!signal.aborted) options.onPageShown?.({ page, pageSize })
      return { items: result.items as LiveAccount[], total: result.total }
    },
    matches: options.matches,
    tasks: options.tasks,
    sources: [
      {
        name: 'rows',
        intervalMs: ROW_BEAT_MS,
        complete: true,
        fetch: async (ids, signal) => {
          const group = options.filters().group
          const result = await adminAPI.accounts.list(1, ids.length, { ids: ids.join(','), lite: '1', ...(group ? { group } : {}) }, { signal })
          return result.items.map((row) => [row.id, row as LiveAccount] as const)
        }
      },
      {
        name: 'quality',
        intervalMs: QUALITY_BEAT_MS,
        fetch: async (ids, signal) => {
          const run = ++qualityRuns
          if (!options.wants.quality()) {
            settleQuality(run)
            return []
          }
          const byId = new Map<number, BackendAccountSummary>()
          try {
            for (const part of chunk(numericIds(ids), QUALITY_MAX_IDS)) {
              const summary = await backendSummary(part, { signal })
              options.onQualityMeta?.({ global_enabled: summary.global_enabled, stale: summary.stale })
              for (const account of summary.accounts) byId.set(account.account_id, account)
            }
          } catch (error) {
            // A failed summary (not an aborted one: its run is owed again) ends the wait: the lock holds no longer (7g-2).
            if (!signal.aborted) releaseQuality(run)
            throw error
          }
          settleQuality(run)
          return ids.map((id) => [id, { quality: byId.get(Number(id)) ?? null }] as const)
        }
      },
      {
        name: 'today',
        intervalMs: STATS_BEAT_MS,
        fetch: async (ids) => {
          if (!options.wants.today()) return []
          const result = await adminAPI.accounts.getBatchTodayStats(numericIds(ids))
          const stats = result.stats ?? {}
          return ids.map((id) => [id, { today_stats: stats[String(id)] ?? emptyTodayStats() }] as const)
        }
      },
      {
        name: 'balance',
        intervalMs: STATS_BEAT_MS,
        fetch: async (ids, signal) => {
          if (balanceForbidden || !options.wants.balance()) return []
          const parts = chunk(numericIds(ids), BALANCE_MAX_IDS)
          const results = await Promise.allSettled(parts.map((part) => adminAPI.accounts.getUpstreamBalances(part, { signal })))
          const patches: Array<readonly [number, Partial<LiveAccount>]> = []
          results.forEach((result, index) => {
            if (result.status === 'rejected') {
              // 403: no more balance requests on this page. A failed chunk keeps its previous values.
              if ((result.reason as { status?: number } | null)?.status === 403) balanceForbidden = true
              return
            }
            const byId = new Map((result.value?.items ?? []).map((item) => [item.account_id, item]))
            for (const id of parts[index]) patches.push([id, { upstream_balance: byId.get(id) ?? null }])
          })
          if (results.every((result) => result.status === 'rejected') && !balanceForbidden) throw (results[0] as PromiseRejectedResult).reason
          return patches
        }
      },
      {
        name: 'usage',
        intervalMs: 0,
        fetch: async (ids) => {
          if (!options.wants.usage()) return []
          const force = forceUsage
          forceUsage = false
          const now = Date.now()
          const supported = numericIds(ids).filter((id) => {
            const row = table.record(id)
            return !!row && supportsBatchUsage(row)
          })
          const due = supported.filter((id) => force || !(now - (usageCache.get(id)?.at ?? 0) < USAGE_CACHE_MS))
          if (due.length) {
            const result = await adminAPI.accounts.getBatchUsage(due, force)
            for (const id of due) {
              const key = String(id)
              usageCache.set(id, { usage: result.usage?.[key] ?? null, error: result.errors?.[key] ?? null, at: now })
            }
          }
          return supported.map((id) => {
            const cached = usageCache.get(id)
            return [id, { usage_info: cached?.usage ?? null, usage_error: cached?.error ?? null }] as const
          })
        }
      }
    ]
  })

  return {
    table,
    /** The server's page count from the last page answer. */
    pages: () => pages,
    /** The next usage run skips the cache (manual refresh). */
    forceUsageRefresh: () => {
      forceUsage = true
    },
    /** A cell's own usage query landed: keep it as the cached value. */
    noteUsage: (id: number, usage: AccountUsageInfo) => {
      usageCache.set(id, { usage, error: null, at: Date.now() })
      table.mergeRows([[id, { usage_info: usage, usage_error: null }]])
    },
    /** A changed account's usage is asked again on the next run. */
    forgetUsage: (ids: number[]) => ids.forEach((id) => usageCache.delete(id)),
    /**
     * Resolves once a quality summary asked for after this call is on the rows (a run already on its way does not
     * count: it may predate the change), or once such a summary fails; an aborted run does not count, the engine runs
     * it again. Leaving the page resolves it too.
     */
    qualityLanded: () =>
      new Promise<void>((resolve) => {
        qualityWaiters.push({ after: qualityRuns, resolve })
      })
  }
}
