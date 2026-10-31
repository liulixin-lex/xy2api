import type { Group } from '@/types'
import { getProviderProfile, platformUsesGateway } from '@/constants/platformCatalog'

/** Show the preference where the public platform contract can route Claude. */
export function groupSupportsAnthropicCacheTTL(group?: Pick<Group, 'platform'> | null): boolean {
  if (!group) return false
  if (group.platform === 'composite' || platformUsesGateway(group.platform, 'anthropic')) return true
  return getProviderProfile(group.platform)?.modes.some((mode) => mode.protocol_rules?.some((rule) =>
    rule.pattern.startsWith('claude-') &&
    (rule.protocol === 'anthropic' || rule.protocols?.includes('anthropic'))
  )) ?? false
}
