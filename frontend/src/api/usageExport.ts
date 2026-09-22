import apiClient from './client'

export type ExportScope = 'user' | 'admin'
export interface UsageExportTask {
  id: string
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'expired' | 'deleted'
  phase: string
  processed_rows: number
  total_rows: number | null
  created_at: string
  snapshot_at: string | null
  expires_at: string | null
  size_bytes: number
  error_code: string
  format: 'csv' | 'xlsx'
}
export const exportBase = (scope: ExportScope) => scope === 'admin' ? '/admin/usage/exports' : '/usage/exports'
export const usageExportAPI = {
  async create(scope: ExportScope, params: Record<string, unknown>, key: string, signal: AbortSignal) {
    return (await apiClient.post<UsageExportTask>(exportBase(scope), params, { signal, headers: { 'Idempotency-Key': key } })).data
  },
  async list(scope: ExportScope, page: number, signal: AbortSignal) {
    return (await apiClient.get<{ items: UsageExportTask[]; total: number }>(exportBase(scope), { signal, params: { page, page_size: 20 } })).data
  },
  async action(scope: ExportScope, id: string, action: 'cancel' | 'delete' | 'download-ticket', signal: AbortSignal) {
    const url = `${exportBase(scope)}/${encodeURIComponent(id)}`
    if (action === 'delete') return (await apiClient.delete(url, { signal })).data
    return (await apiClient.post<{ url?: string }>(`${url}/${action}`, {}, { signal })).data
  }
}
