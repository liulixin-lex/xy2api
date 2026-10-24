import { apiClient } from '@/api/client'
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

export async function getNorthStar(query: NorthStarQuery): Promise<NorthStarResponse> {
  const params: Record<string, string> = { window: query.window, platform: query.platform }
  if (query.granularity) params.granularity = query.granularity
  if (query.cost_view) params.cost_view = query.cost_view
  const { data } = await apiClient.get<NorthStarResponse>(`${basePath}/northstar`, { params })
  return data
}

export async function getBreakdown(query: BreakdownQuery): Promise<BreakdownResponse> {
  const { data } = await apiClient.get<BreakdownResponse>(`${basePath}/breakdown`, {
    params: {
      dimension: query.dimension,
      window: query.window,
      platform: query.platform,
      sort: query.sort,
      limit: query.limit ?? 10,
      ...(query.cost_view ? { cost_view: query.cost_view } : {}),
    },
  })
  return data
}

export async function getTraceStats(query: TraceStatsQuery): Promise<TraceStatsResponse> {
  const { data } = await apiClient.get<TraceStatsResponse>(`${basePath}/stats`, {
    params: { window: query.window, platform: query.platform },
  })
  return data
}

export async function listExperiments(): Promise<ExperimentsResponse> {
  const { data } = await apiClient.get<ExperimentsResponse>(`${basePath}/experiments`)
  return data
}

export const liveCachePanelClient: CachePanelClient = {
  getNorthStar,
  getBreakdown,
  getTraceStats,
  listExperiments,
}
