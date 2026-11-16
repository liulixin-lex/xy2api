import { describe, expect, it } from 'vitest'
import { canAccessManagementMCP, showManagementMCPMenu } from '../managementMcpAccess'

describe('management MCP role access', () => {
  it('fails closed for missing, disabled and unknown roles', () => {
    for (const role of ['admin', 'operator', 'user', 'unknown', undefined]) {
      expect(canAccessManagementMCP(null, role)).toBe(false)
      expect(showManagementMCPMenu({ mcp_enabled: false, mcp_operator_enabled: true, mcp_user_enabled: true }, role)).toBe(false)
    }
    expect(canAccessManagementMCP({ mcp_enabled: true }, 'unknown')).toBe(false)
  })

  it('keeps root access independent of the ordinary-role switches', () => {
    expect(canAccessManagementMCP({ mcp_enabled: true }, 'admin')).toBe(true)
    expect(showManagementMCPMenu({ mcp_enabled: true }, 'admin')).toBe(true)
    expect(canAccessManagementMCP({ mcp_enabled: true }, 'operator')).toBe(false)
    expect(canAccessManagementMCP({ mcp_enabled: true }, 'user')).toBe(false)
  })

  it.each(['operator', 'user'] as const)('separates %s access from menu visibility', role => {
    for (const allowed of [false, true]) {
      for (const visible of [false, true]) {
        const settings = {
          mcp_enabled: true,
          mcp_operator_enabled: role === 'operator' && allowed,
          mcp_user_enabled: role === 'user' && allowed,
          mcp_operator_menu_visible: visible,
          mcp_user_menu_visible: visible,
        }
        expect(canAccessManagementMCP(settings, role)).toBe(allowed)
        expect(showManagementMCPMenu(settings, role)).toBe(allowed && visible)
      }
    }
  })
})
