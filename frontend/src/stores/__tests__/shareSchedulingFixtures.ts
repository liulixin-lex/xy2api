import type {
  ShareGroupSummary,
  ShareMember,
  ShareMetrics,
  SharePolicyView,
  SharePreview,
  ShareReport
} from '@/api/admin/shareScheduling'

export function shareMember(id: number, overrides: Partial<ShareMember> = {}): ShareMember {
  return {
    account_id: id,
    name: `acct-${id}`,
    platform: 'openai',
    type: 'oauth',
    status: 'active',
    schedulable: true,
    healthy: true,
    reason: null,
    recover_at: null,
    priority: 1,
    concurrency: 10,
    load_factor: null,
    effective_load_factor: 10,
    other_group_ids: [],
    expected_share: 0.5,
    share_if_healthy: 0.5,
    actual_requests_1h: 100,
    actual_share_1h: 0.5,
    recent_requests_1h: 100,
    recent_errors_1h: 0,
    recent_error_rate_1h: 0,
    ...overrides
  }
}

export function shareView(groupId: number, overrides: Partial<SharePolicyView> = {}): SharePolicyView {
  const members = overrides.members ?? [shareMember(1), shareMember(2)]
  return {
    group_id: groupId,
    group_name: `group-${groupId}`,
    platform: 'openai',
    scheduler: 'sub2api',
    session_keep: true,
    legacy_shadow: false,
    effective: false,
    version: 1,
    updated_by: null,
    updated_at: '2026-10-20T08:00:00Z',
    enabled: true,
    invalid: null,
    active_priority: 1,
    ...overrides,
    members
  }
}

export function shareGroup(groupId: number, overrides: Partial<ShareGroupSummary> = {}): ShareGroupSummary {
  return {
    group_id: groupId,
    name: `group-${groupId}`,
    platform: 'openai',
    scheduler: 'sub2api',
    session_keep: true,
    legacy_shadow: false,
    configured: false,
    invalid: false,
    effective: false,
    version: 0,
    updated_at: null,
    member_count: 2,
    available_count: 2,
    requests_1h: 0,
    cache_hit_ratio_1h: null,
    switch_loss_ratio_1h: null,
    ...overrides
  }
}

export function shareMetrics(overrides: Partial<ShareMetrics> = {}): ShareMetrics {
  return {
    requests: 1000,
    cache_hit_ratio: 0.8,
    ttft_p50_ms: 4000,
    ttft_p95_ms: 20000,
    ttft_samples: 800,
    errors: 20,
    error_rate: 0.02,
    attribution_samples: 250,
    switch_rate: 0.01,
    switch_loss_ratio: 0.002,
    ...overrides
  }
}

export function shareReport(groupId: number, overrides: Partial<ShareReport> = {}): ShareReport {
  return {
    group_id: groupId,
    scheduler: 'share',
    session_keep: true,
    version: 2,
    from: '2026-10-20T08:00:00Z',
    to: '2026-10-20T09:00:00Z',
    window_seconds: 3600,
    current: shareMetrics(),
    baseline: shareMetrics(),
    baseline_from: '2026-10-20T06:00:00Z',
    baseline_to: '2026-10-20T07:00:00Z',
    baseline_unavailable: null,
    accounts: [],
    details: { cache_classes: { cache_hit: { requests: 200, tokens: 800000 }, miss_new_session: { requests: 50, tokens: 200000 } } },
    counters_error: null,
    counter_precision: 'minute',
    ...overrides
  }
}

export function sharePreview(overrides: Partial<SharePreview> = {}): SharePreview {
  return {
    scheduler: 'share',
    session_keep: true,
    enabled: true,
    effective_after: true,
    active_priority: 1,
    members: [shareMember(1, { expected_share: 0.75 }), shareMember(2, { expected_share: 0.25 }), shareMember(3, { priority: 2, expected_share: 0, share_if_healthy: 0 })],
    warnings: [],
    ...overrides
  }
}
