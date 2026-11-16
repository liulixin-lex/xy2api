import { apiClient } from '../client'

export interface ManagementMCPSettings {
  enabled: boolean
  public_url: string
  allow_operators: boolean
  allow_users: boolean
  show_operator_menu: boolean
  show_user_menu: boolean
}

export async function getManagementMCPSettings(): Promise<ManagementMCPSettings> {
  const { data } = await apiClient.get<ManagementMCPSettings>('/admin/settings/management-mcp')
  return data
}

export async function updateManagementMCPSettings(input: ManagementMCPSettings): Promise<ManagementMCPSettings> {
  const { data } = await apiClient.put<ManagementMCPSettings>('/admin/settings/management-mcp', input)
  return data
}
