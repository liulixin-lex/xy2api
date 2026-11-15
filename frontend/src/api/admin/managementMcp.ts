import { apiClient } from '../client'

export interface ManagementMCPSettings {
  enabled: boolean
  public_url: string
}

export async function getManagementMCPSettings(): Promise<ManagementMCPSettings> {
  const { data } = await apiClient.get<ManagementMCPSettings>('/admin/settings/management-mcp')
  return data
}

export async function updateManagementMCPSettings(input: ManagementMCPSettings): Promise<ManagementMCPSettings> {
  const { data } = await apiClient.put<ManagementMCPSettings>('/admin/settings/management-mcp', input)
  return data
}
