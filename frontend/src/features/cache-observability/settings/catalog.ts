// Presentation catalog for the cache policy keys (DESIGN §11.2, §11.7).
// It holds order and copy only. Field types, ranges, enums and
// units come from the server `schema`; nothing here constrains values.
import type { PolicyKey } from './types'

export interface PolicyCardMeta {
  key: Exclude<PolicyKey, 'master'>
  settingKey: string
  /** Expected master gating; the server's `gated_by_master` wins when present. */
  gated: boolean
}

/** Cards ordered from low to high risk (§11.7). */
export const POLICY_CARDS: PolicyCardMeta[] = [
  { key: 'observability', settingKey: 'cache_observability_policy', gated: false },
  { key: 'northstar', settingKey: 'cache_northstar_policy', gated: false },
  { key: 'upstream_identity', settingKey: 'cache_upstream_identity_policy', gated: true },
  { key: 'anthropic_identity', settingKey: 'cache_anthropic_identity_policy', gated: true },
  { key: 'routing', settingKey: 'cache_routing_policy', gated: true },
]

export const MASTER_KEY: PolicyKey = 'master'
export const MASTER_SETTING_KEY = 'cache_feature_master'

/** i18n leaf for a field path: dots become underscores (vue-i18n paths use dots). */
export function fieldSlug(path: string): string {
  return path.replace(/\./g, '_')
}

/** DOM id of a field's control, shared by the field row and the "go to the first error" action. */
export function controlIdFor(key: string, path: string): string {
  return `cache-policy-${`${key}-${path}`.replace(/[^a-z0-9]+/gi, '-')}`
}

export function fieldCopyKey(key: string, path: string, part: 'label' | 'description' | 'risk'): string {
  return `admin.cacheSettings.fields.${key}.${fieldSlug(path)}.${part}`
}

/** Defaults written by the runtime cache; used only for the "takes effect by" estimate before the server answers. */
export const DEFAULT_REFRESH_SECONDS = 60
