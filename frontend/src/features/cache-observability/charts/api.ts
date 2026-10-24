import { apiClient } from '@/api/client'
import { checkBreakdown, checkExperiments, checkNorthStar, checkTraceStats } from './guard'
import type {
  BreakdownQuery,
  BreakdownResponse,
  CachePanelClient,
  ExperimentsResponse,
  NorthStarQuery,
  NorthStarResponse,
  TraceStatsQuery,
  TraceStatsResponse,
} from './types'

const basePath = '/admin/cache'

/**
 * Contract P-10: each endpoint takes only its own parameters and answers 400 to anything else. The platform
 * client adds `timezone` to every GET, so the URL leaves it out here (FE-Q15).
 */
const paramsSerializer = {
  serialize: (params: Record<string, unknown>) =>
    new URLSearchParams(
      Object.entries(params)
        .filter(([key, value]) => key !== 'timezone' && value !== undefined && value !== null)
        .map(([key, value]) => [key, String(value)]),
    ).toString(),
}

export async function getNorthStar(query: NorthStarQuery): Promise<NorthStarResponse> {
  const params: Record<string, string> = { window: query.window, platform: query.platform }
  if (query.granularity) params.granularity = query.granularity
  if (query.cost_view) params.cost_view = query.cost_view
  const { data } = await apiClient.get<NorthStarResponse>(`${basePath}/northstar`, { params, paramsSerializer })
  return checkNorthStar(data)
}

export async function getBreakdown(query: BreakdownQuery): Promise<BreakdownResponse> {
  const limit = query.limit ?? 10
  const { data } = await apiClient.get<BreakdownResponse>(`${basePath}/breakdown`, {
    params: {
      dimension: query.dimension,
      window: query.window,
      platform: query.platform,
      sort: query.sort,
      limit,
      ...(query.cost_view ? { cost_view: query.cost_view } : {}),
    },
    paramsSerializer,
  })
  return checkBreakdown(data, limit)
}

export async function getTraceStats(query: TraceStatsQuery): Promise<TraceStatsResponse> {
  const { data } = await apiClient.get<TraceStatsResponse>(`${basePath}/stats`, {
    params: { window: query.window, platform: query.platform },
    paramsSerializer,
  })
  return checkTraceStats(data)
}

export async function listExperiments(): Promise<ExperimentsResponse> {
  const { data } = await apiClient.get<ExperimentsResponse>(`${basePath}/experiments`, { paramsSerializer })
  return checkExperiments(data)
}

export const liveCachePanelClient: CachePanelClient = {
  getNorthStar,
  getBreakdown,
  getTraceStats,
  listExperiments,
}
