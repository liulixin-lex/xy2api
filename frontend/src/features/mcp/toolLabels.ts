const toolKeys: Record<string, string> = {
  'xy2api.requests.search': 'requestsSearch',
  'xy2api.requests.get_detail': 'requestsDetail',
  'xy2api.requests.get_attempts': 'requestsAttempts',
  'xy2api.requests.search_ingress_rejections': 'ingressRejections',
  'xy2api.accounts.search': 'accountsSearch',
  'xy2api.accounts.get_health': 'accountsHealth',
  'xy2api.groups.search': 'groupsSearch',
  'xy2api.groups.get_usage_summary': 'groupsUsage',
  'xy2api.channels.search': 'channelsSearch',
  'xy2api.channels.get_metrics': 'channelsMetrics',
  'xy2api.ops.get_error_breakdown': 'errorBreakdown',
  'xy2api.ops.get_trends': 'trends',
  'xy2api.ops.get_alerts': 'alerts',
  'xy2api.ops.get_system_events': 'systemEvents',
  'xy2api.ops.get_realtime_snapshot': 'realtime',
  'xy2api.ops.get_concurrency_snapshot': 'concurrency',
  'xy2api.routing.get_quality_state': 'quality',
  'xy2api.users.search': 'usersSearch',
  'xy2api.users.get_usage_summary': 'usersUsage',
  'xy2api.users.get_subscriptions': 'usersSubscriptions',
  'xy2api.audit.search': 'audit',
  'xy2api.security.search_prompt_audit': 'promptAudit',
  'xy2api.me.get_profile': 'meProfile',
  'xy2api.me.search_usage': 'meUsage',
  'xy2api.me.get_usage_detail': 'meUsageDetail',
  'xy2api.me.get_error_history': 'meErrors',
  'xy2api.me.get_error_detail': 'meErrorDetail',
  'xy2api.me.get_subscription_status': 'meSubscriptions',
  'xy2api.me.get_quota_status': 'meQuota'
}

export function toolLabel(tool: string, translate: (key: string) => string): string {
  const key = toolKeys[tool]
  return key ? translate(`mcp.tools.${key}`) : tool
}
