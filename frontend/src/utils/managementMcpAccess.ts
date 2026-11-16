import type { PublicSettings } from '@/types'

/** Menu visibility is independent of authorization; access is always required. */
export function canAccessManagementMCP(settings: Partial<PublicSettings> | null | undefined, role: string | undefined): boolean {
  if (settings?.mcp_enabled !== true) return false
  if (role === 'admin') return true
  if (role === 'operator') return settings.mcp_operator_enabled === true
  return role === 'user' && settings.mcp_user_enabled === true
}

export function showManagementMCPMenu(settings: Partial<PublicSettings> | null | undefined, role: string | undefined): boolean {
  if (!canAccessManagementMCP(settings, role)) return false
  if (role === 'admin') return true
  return role === 'operator' ? settings?.mcp_operator_menu_visible === true : settings?.mcp_user_menu_visible === true
}
