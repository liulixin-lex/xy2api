import apiClient from '@/api/client'

export interface ConsentPreview {
  request_id: string
  client_id: string
  client_name: string
  client_name_verified: boolean
  redirect_uri: string
  resource: string
  tools: string[]
  expires_at: string
}

export interface MCPConnection {
  id: string
  client_id: string
  client_name: string
  resource: string
  tools: string[]
  created_at: string
  expires_at: string
  revoked_at: string | null
}

export async function previewConsent(requestID: string, signal: AbortSignal) {
  const { data } = await apiClient.post<ConsentPreview>('/mcp/consent/preview', { request_id: requestID }, { signal })
  return data
}

export async function decideConsent(requestID: string, approve: boolean, tools: string[], signal: AbortSignal) {
  const { data } = await apiClient.post<{ redirect_url: string }>('/mcp/consent', {
    request_id: requestID,
    approve,
    tools: approve ? tools : []
  }, { signal })
  return data
}

export async function listConnections(signal: AbortSignal) {
  const { data } = await apiClient.get<{ items: MCPConnection[]; resource: string }>('/mcp/connections', { signal })
  return data
}

export async function revokeConnection(id: string, signal: AbortSignal) {
  await apiClient.delete(`/mcp/connections/${encodeURIComponent(id)}`, { signal })
}

export function errorStatus(error: unknown): number | undefined {
  return typeof error === 'object' && error !== null && 'status' in error
    ? Number(error.status)
    : undefined
}

// The authorization server supplies this URL. Keep browser navigation bound to
// the redirect shown in the consent preview, even if a response is malformed.
export function verifiedConsentRedirect(candidate: string, registered: string): string | null {
  try {
    const target = new URL(candidate)
    const expected = new URL(registered)
    const loopback = ['localhost', '[::1]'].includes(expected.hostname) || /^127(?:\.\d{1,3}){3}$/.test(expected.hostname)
    if (expected.protocol !== 'https:' && !(expected.protocol === 'http:' && loopback)) return null
    if (target.username || target.password || target.hash || expected.username || expected.password || expected.hash) return null
    if (target.origin !== expected.origin || target.pathname !== expected.pathname) return null
    // Preserve every registered query value, including repeated values.
    for (const key of new Set(expected.searchParams.keys())) {
      if (JSON.stringify(target.searchParams.getAll(key)) !== JSON.stringify(expected.searchParams.getAll(key))) return null
    }
    return target.href
  } catch {
    return null
  }
}
