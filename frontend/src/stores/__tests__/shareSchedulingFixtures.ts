import type { ShareMember, SharePolicy, SharePolicyView } from '@/api/admin/shareScheduling'

export function sharePolicy(overrides: Partial<SharePolicy> = {}): SharePolicy {
  return {
    schema: 1,
    scheduler: 'sub2api',
    accounts: [],
    defaults: { tier: 'account_priority', weight: 'concurrency' },
    affinity: {
      enabled: true,
      key: 'auto',
      ttl_seconds: 3600,
      wait: { mode: 'auto', ms_per_1k_tokens: 100, max_ms: 15000, max_waiting: 3 },
      on_spill: 'rebind',
      soft_spill_max_seconds: 300
    },
    cache: { ttl_seconds: 0, realign: 'cold' },
    tiers: { on_saturated: 'spill', on_failed: 'next_tier', return: 'cold', stable_seconds: 120, return_deadline_seconds: 0 },
    balance: { unit: 'sessions', correction: 'off', window_minutes: 60, min_volume: 50 },
    ...overrides
  }
}

export function shareMember(id: number, overrides: Partial<ShareMember> = {}): ShareMember {
  return {
    account_id: id,
    name: `acct-${id}`,
    platform: 'anthropic',
    type: 'oauth',
    priority: 1,
    concurrency: 10,
    status: 'active',
    schedulable: true,
    healthy: true,
    tier: 1,
    weight: 10,
    configured: false,
    drain: false,
    cache_domain: `account:${id}`,
    target_share: 0,
    effective_share: 0,
    capacity_share: 0,
    ...overrides
  }
}

export function shareView(groupId: number, overrides: Partial<SharePolicyView> = {}): SharePolicyView {
  const members = overrides.members ?? [shareMember(1), shareMember(2)]
  return {
    group_id: groupId,
    group_name: `group-${groupId}`,
    platform: 'anthropic',
    version: 1,
    scheduler: 'sub2api',
    effective: false,
    policy: sharePolicy(),
    active_tier: 1,
    ...overrides,
    members
  }
}
