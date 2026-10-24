import { computed, reactive, ref, shallowRef, watch } from 'vue'
import { adminAPI } from '@/api/admin'
import { createChartsClient, resolveDataSource, type ChartsDataSource } from './dataSource'
import { ContractRejection } from './guard'
import type {
  BreakdownDimension,
  BreakdownResponse,
  BreakdownSort,
  CachePanelClient,
  CostView,
  ExperimentsResponse,
  NorthStarResponse,
  PanelPlatform,
  SummaryWindow,
  TraceStatsResponse,
  TraceWindow,
} from './types'

/** `rejected`: the answer carried data the panel must not show (guard.ts), so none of it is rendered. */
export type SectionStatus = 'idle' | 'loading' | 'ready' | 'error' | 'unavailable' | 'not_ready' | 'rejected'

export interface Section<T> {
  status: SectionStatus
  data: T | null
  /** True while a refetch runs over data that is still on screen (held at reduced opacity). */
  refreshing: boolean
  error: string | null
}

function section<T>(): Section<T> {
  return { status: 'idle', data: null, refreshing: false, error: null }
}

interface RejectedApiError {
  status?: number
  code?: string | number
  reason?: string
  message?: string
}

function classify(error: unknown): { status: SectionStatus; message: string | null } {
  if (error instanceof ContractRejection) return { status: 'rejected', message: null }
  const e = (error && typeof error === 'object' ? error : {}) as RejectedApiError
  if (e.status === 404) return { status: 'unavailable', message: null }
  // §4 and contract P-9 name it by reason; a bare 503 without one is read the same way.
  if (e.reason === 'CACHE_ROLLUP_NOT_READY' || (e.status === 503 && !e.reason)) return { status: 'not_ready', message: null }
  return { status: 'error', message: typeof e.message === 'string' ? e.message : null }
}

/** The part of an admin group the panel needs to tell fallback groups (CACHE_PANEL_API §7.1). */
export interface FallbackSource {
  id: number
  availability_fallback_group_id?: number | null
}

export interface ChartsDeps {
  client?: CachePanelClient
  source?: ChartsDataSource
  loadGroups?: () => Promise<FallbackSource[]>
}

export function useCacheCharts(deps: ChartsDeps = {}) {
  const source = ref<ChartsDataSource>(deps.source ?? resolveDataSource())
  let clientPromise: Promise<CachePanelClient> | null = deps.client ? Promise.resolve(deps.client) : null
  const client = () => (clientPromise ??= createChartsClient(source.value))

  const filters = reactive({
    summaryWindow: '7d' as SummaryWindow,
    traceWindow: '24h' as TraceWindow,
    platform: 'all' as PanelPlatform,
    costView: 'list' as CostView,
    dimension: 'group' as BreakdownDimension,
    sort: 'opportunity' as BreakdownSort,
  })

  const northstar = reactive(section<NorthStarResponse>())
  const breakdown = reactive(section<BreakdownResponse>())
  const stats = reactive(section<TraceStatsResponse>())
  const experiments = reactive(section<ExperimentsResponse>())
  const lastRefreshed = shallowRef<Date | null>(null)

  // A group referenced by any group's availability_fallback_group_id is a fallback group; the API no longer says so.
  const loadGroups = deps.loadGroups ?? (() => adminAPI.groups.getAllIncludingInactive())
  const groups = shallowRef<FallbackSource[]>([])
  let groupsRequested = false
  async function loadGroupsOnce() {
    if (groupsRequested) return
    groupsRequested = true
    try {
      groups.value = await loadGroups()
    } catch {
      // Without the list the ranking simply shows no fallback tags; the next full load tries again.
      groupsRequested = false
    }
  }
  const fallbackGroupIds = computed(() => {
    const ids = new Set<number>()
    for (const g of groups.value) if (typeof g.availability_fallback_group_id === 'number') ids.add(g.availability_fallback_group_id)
    return ids
  })

  /** null until the first northstar answer; false when this backend has no chart endpoints (404). */
  const available = computed<boolean | null>(() => {
    if (northstar.status === 'unavailable') return false
    if (northstar.status === 'ready' || northstar.status === 'not_ready' || northstar.status === 'error' || northstar.status === 'rejected') return true
    return northstar.data ? true : null
  })

  async function run<T>(target: Section<T>, fetcher: (c: CachePanelClient) => Promise<T>) {
    target.refreshing = target.data !== null
    if (!target.refreshing) target.status = 'loading'
    target.error = null
    try {
      const data = await fetcher(await client())
      target.data = data as T
      target.status = 'ready'
    } catch (error) {
      const result = classify(error)
      target.status = result.status
      target.error = result.message
      if (result.status !== 'error') target.data = null
    } finally {
      target.refreshing = false
    }
  }

  const loadNorthStar = () =>
    run(northstar, (c) => c.getNorthStar({ window: filters.summaryWindow, platform: filters.platform, cost_view: filters.costView }))
  const loadBreakdown = () =>
    run(breakdown, (c) =>
      c.getBreakdown({
        dimension: filters.dimension,
        window: filters.summaryWindow,
        platform: filters.platform,
        sort: filters.sort,
        cost_view: filters.costView,
      }),
    )
  const loadStats = () => run(stats, (c) => c.getTraceStats({ window: filters.traceWindow, platform: filters.platform }))
  const loadExperiments = () => run(experiments, (c) => c.listExperiments())

  /** Probes the chart endpoints through northstar first, then loads the rest only if they exist. */
  async function loadAll() {
    await loadNorthStar()
    if (available.value === false) return
    void loadGroupsOnce()
    await Promise.all([loadBreakdown(), loadStats(), loadExperiments()])
    lastRefreshed.value = new Date()
  }

  watch(() => [filters.summaryWindow, filters.platform, filters.costView], () => {
    if (available.value) void Promise.all([loadNorthStar(), loadBreakdown()])
  })
  watch(() => [filters.dimension, filters.sort], () => {
    if (available.value) void loadBreakdown()
  })
  watch(() => [filters.traceWindow, filters.platform], () => {
    if (available.value) void loadStats()
  })

  type Filters = typeof filters
  function setFilter<K extends keyof Filters>(key: K, value: Filters[K]) {
    filters[key] = value
  }

  const autoRefreshSeconds = ref(0)
  function setAutoRefresh(seconds: number) {
    autoRefreshSeconds.value = Number.isFinite(seconds) && seconds > 0 ? seconds : 0
  }
  let timer: ReturnType<typeof setInterval> | null = null
  function stopAutoRefresh() {
    if (timer) clearInterval(timer)
    timer = null
  }
  watch(autoRefreshSeconds, (seconds) => {
    stopAutoRefresh()
    if (seconds > 0) {
      timer = setInterval(() => {
        if (typeof document !== 'undefined' && document.hidden) return
        void loadAll()
      }, seconds * 1000)
    }
  })

  return {
    source,
    filters,
    northstar,
    breakdown,
    stats,
    experiments,
    available,
    fallbackGroupIds,
    lastRefreshed,
    autoRefreshSeconds,
    setFilter,
    setAutoRefresh,
    loadAll,
    loadNorthStar,
    loadBreakdown,
    loadStats,
    loadExperiments,
    stopAutoRefresh,
  }
}

export type CacheChartsState = ReturnType<typeof useCacheCharts>
