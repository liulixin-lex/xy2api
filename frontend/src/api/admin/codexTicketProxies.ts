import { apiClient } from '../client'

export interface TicketProxyProbe {
  success: boolean
  checked_at: string
  latency_ms: number
  ip_address?: string
  country?: string
  country_code?: string
  message?: string
}
export interface TicketProxy {
  id: string
  name: string
  enabled: boolean
  revision: string
  display: string
  url?: string
  probe?: TicketProxyProbe
  last_success?: TicketProxyProbe
  last_acquisition?: string
}
export interface TicketProxyPool { revision: string; entries: TicketProxy[] }
const path = '/admin/settings/codex-ticket/proxies'
export async function getTicketProxyPool(): Promise<TicketProxyPool> {
  return (await apiClient.get<TicketProxyPool>(path)).data
}
export async function saveTicketProxyPool(pool: TicketProxyPool): Promise<TicketProxyPool> {
  return (await apiClient.put<TicketProxyPool>(path, {
    expected_revision: pool.revision,
    entries: pool.entries.map(({ id, name, enabled, url }) => ({ id, name, enabled, ...(url?.trim() ? { url: url.trim() } : {}) }))
  })).data
}
export async function testTicketProxy(entry: TicketProxy): Promise<TicketProxyProbe> {
  const input = entry.url?.trim() ? { url: entry.url.trim() } : { id: entry.id, expected_revision: entry.revision }
  return (await apiClient.post<TicketProxyProbe>(`${path}/test`, input, { timeout: 15000 })).data
}
