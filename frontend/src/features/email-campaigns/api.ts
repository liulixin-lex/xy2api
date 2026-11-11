import { apiClient } from '@/api/client'
import type { PaginatedResponse } from '@/types'
import type {
  AddSuppressionsInput,
  AddSuppressionsResult,
  Campaign,
  CampaignDetail,
  CampaignListQuery,
  CampaignSettings,
  ContentInput,
  CreateCampaignInput,
  PreviewRequest,
  PreviewResult,
  Recipient,
  RecipientListQuery,
  RenderResult,
  RetryFailedResult,
  SendLimits,
  Suppression,
  SuppressionListQuery,
  TestSendInput,
} from './types'

const basePath = '/admin/email-campaigns'

/** The server caps page_size at 100. */
export const MAX_PAGE_SIZE = 100

function clampPageSize(size: number | undefined): number | undefined {
  if (size === undefined) return undefined
  return Math.max(1, Math.min(MAX_PAGE_SIZE, Math.floor(size)))
}

/** Drop empty strings and undefined so they never reach the query string. */
function cleanParams(params: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    out[key] = value
  }
  return out
}

interface RequestOptions {
  signal?: AbortSignal
}

export async function getSettings(): Promise<CampaignSettings> {
  const { data } = await apiClient.get<CampaignSettings>(`${basePath}/settings`)
  return data
}

export async function updateSettings(limits: SendLimits): Promise<CampaignSettings> {
  const { data } = await apiClient.put<CampaignSettings>(`${basePath}/settings`, limits)
  return data
}

export async function previewAudience(req: PreviewRequest, options: RequestOptions = {}): Promise<PreviewResult> {
  const { data } = await apiClient.post<PreviewResult>(
    `${basePath}/preview`,
    { ...req, page_size: clampPageSize(req.page_size) },
    { signal: options.signal },
  )
  return data
}

export async function renderContent(input: ContentInput, options: RequestOptions = {}): Promise<RenderResult> {
  const { data } = await apiClient.post<RenderResult>(`${basePath}/render`, input, { signal: options.signal })
  return data
}

export async function sendTest(input: TestSendInput): Promise<{ to: string }> {
  const { data } = await apiClient.post<{ to: string }>(`${basePath}/test-send`, input)
  return data
}

/** Create and start a campaign. Step-up protected: call through useStepUp().run. */
export async function createCampaign(input: CreateCampaignInput): Promise<Campaign> {
  const { data } = await apiClient.post<Campaign>(basePath, input)
  return data
}

export async function listCampaigns(
  query: CampaignListQuery = {},
  options: RequestOptions = {},
): Promise<PaginatedResponse<Campaign>> {
  const { data } = await apiClient.get<PaginatedResponse<Campaign>>(basePath, {
    params: cleanParams({ ...query, page_size: clampPageSize(query.page_size) }),
    signal: options.signal,
  })
  return data
}

export async function getCampaign(id: number): Promise<CampaignDetail> {
  const { data } = await apiClient.get<CampaignDetail>(`${basePath}/${id}`)
  return data
}

export async function listRecipients(
  id: number,
  query: RecipientListQuery = {},
  options: RequestOptions = {},
): Promise<PaginatedResponse<Recipient>> {
  const { data } = await apiClient.get<PaginatedResponse<Recipient>>(`${basePath}/${id}/recipients`, {
    params: cleanParams({ ...query, page_size: clampPageSize(query.page_size) }),
    signal: options.signal,
  })
  return data
}

export async function pauseCampaign(id: number): Promise<Campaign> {
  const { data } = await apiClient.post<Campaign>(`${basePath}/${id}/pause`)
  return data
}

export async function resumeCampaign(id: number): Promise<Campaign> {
  const { data } = await apiClient.post<Campaign>(`${basePath}/${id}/resume`)
  return data
}

export async function cancelCampaign(id: number): Promise<Campaign> {
  const { data } = await apiClient.post<Campaign>(`${basePath}/${id}/cancel`)
  return data
}

export async function retryFailed(id: number): Promise<RetryFailedResult> {
  const { data } = await apiClient.post<RetryFailedResult>(`${basePath}/${id}/retry-failed`)
  return data
}

export async function listSuppressions(
  query: SuppressionListQuery = {},
  options: RequestOptions = {},
): Promise<PaginatedResponse<Suppression>> {
  const { data } = await apiClient.get<PaginatedResponse<Suppression>>(`${basePath}/suppressions`, {
    params: cleanParams({ ...query, page_size: clampPageSize(query.page_size) }),
    signal: options.signal,
  })
  return data
}

export async function addSuppressions(input: AddSuppressionsInput): Promise<AddSuppressionsResult> {
  const { data } = await apiClient.post<AddSuppressionsResult>(`${basePath}/suppressions`, input)
  return data
}

export async function removeSuppression(id: number): Promise<{ removed: boolean }> {
  const { data } = await apiClient.delete<{ removed: boolean }>(`${basePath}/suppressions/${id}`)
  return data
}
