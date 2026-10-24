import { apiClient } from '@/api/client'
import { checkBreakdown, checkExperiments, checkNorthStar, checkTraceStats } from './guard'
import type {
  BreakdownQuery,
  BreakdownResponse,
  CachePanelClient,
  ExperimentsResponse,
  NorthStarQuery,
  NorthStarResponse,
  RequestOptions,
  TraceStatsQuery,
  TraceStatsResponse,
} from './types'

const basePath = '/admin/cache'

/**
 * Contract P-10: each endpoint takes only its own parameters and answers 400 to anything else. The platform
 * client adds `timezone` to every GET, and the contract refuses it too (R15), so the URL leaves it out here.
 */
const paramsSerializer = {
  serialize: (params: Record<string, unknown>) =>
    new URLSearchParams(
      Object.entries(params)
        .filter(([key, value]) => key !== 'timezone' && value !== undefined && value !== null)
        .map(([key, value]) => [key, String(value)]),
    ).toString(),
}

export async function getNorthStar(query: NorthStarQuery, options: RequestOptions = {}): Promise<NorthStarResponse> {
  const params: Record<string, string> = { window: query.window, platform: query.platform }
  if (query.granularity) params.granularity = query.granularity
  if (query.cost_view) params.cost_view = query.cost_view
  const { data } = await apiClient.get<NorthStarResponse>(`${basePath}/northstar`, { params, paramsSerializer, signal: options.signal })
  return checkNorthStar(data)
}

export async function getBreakdown(query: BreakdownQuery, options: RequestOptions = {}): Promise<BreakdownResponse> {
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
    signal: options.signal,
  })
  return checkBreakdown(data, limit)
}

export async function getTraceStats(query: TraceStatsQuery, options: RequestOptions = {}): Promise<TraceStatsResponse> {
  const { data } = await apiClient.get<TraceStatsResponse>(`${basePath}/stats`, {
    params: { window: query.window, platform: query.platform },
    paramsSerializer,
    signal: options.signal,
  })
  return checkTraceStats(data)
}

export async function listExperiments(options: RequestOptions = {}): Promise<ExperimentsResponse> {
  const { data } = await apiClient.get<ExperimentsResponse>(`${basePath}/experiments`, { paramsSerializer, signal: options.signal })
  return checkExperiments(data)
}

export const liveCachePanelClient: CachePanelClient = {
  getNorthStar,
  getBreakdown,
  getTraceStats,
  listExperiments,
}
