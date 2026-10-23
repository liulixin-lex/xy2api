import type { PoliciesResponse, PolicyEntry, ValidateResult } from '../types'

export function observabilityEntry(overrides: Partial<PolicyEntry> = {}): PolicyEntry {
  return {
    key: 'observability',
    setting_key: 'cache_observability_policy',
    available: true,
    risk: 'low',
    gated_by_master: false,
    stored: {
      rev: 7,
      updated_at: '2026-10-23T07:58:40Z',
      updated_by: { user_id: 1, email: 'ops-admin' },
      reason: 'enable observation',
      spec: { enabled: false, sample_percent: 100, chain_percent: 100, byte_budget_mib: 64, rate_mib_per_s: 32 },
    },
    loaded: { rev: 7, loaded_at: '2026-10-23T07:58:40Z' },
    effective: { state: 'off', active: false, blockers: [], warnings: [], clipped_fields: [] },
    defaults: { enabled: false, sample_percent: 100, chain_percent: 100, byte_budget_mib: 64, rate_mib_per_s: 32 },
    schema: [
      { field: 'enabled', type: 'bool' },
      { field: 'sample_percent', type: 'int', min: 0, max: 100, unit: 'percent' },
      { field: 'chain_percent', type: 'int', min: 0, max: 100, unit: 'percent' },
      { field: 'byte_budget_mib', type: 'int', min: 8, max: 64, unit: 'MiB', cap_source: 'gateway.cache_trace.max_byte_budget_mib' },
      { field: 'rate_mib_per_s', type: 'int', min: 1, max: 32, unit: 'MiB/s', cap_source: 'gateway.cache_trace.max_rate_mib_per_s' },
    ],
    ...overrides,
  }
}

export function masterEntry(enabled = false): PolicyEntry {
  return {
    key: 'master',
    setting_key: 'cache_feature_master',
    available: true,
    risk: 'high',
    gated_by_master: false,
    stored: { rev: 2, updated_at: null, updated_by: null, reason: null, spec: { enabled } },
    loaded: { rev: 2, loaded_at: '2026-10-23T07:00:00Z' },
    effective: { state: enabled ? 'on' : 'off', active: enabled, blockers: [], warnings: [], clipped_fields: [] },
    defaults: { enabled: false },
    schema: [{ field: 'enabled', type: 'bool' }],
  }
}

export function identityEntry(overrides: Partial<PolicyEntry> = {}): PolicyEntry {
  return {
    key: 'upstream_identity',
    setting_key: 'cache_upstream_identity_policy',
    available: true,
    risk: 'high',
    gated_by_master: true,
    stored: {
      rev: 3,
      updated_at: '2026-10-23T06:00:00Z',
      updated_by: { user_id: 1, email: 'ops-admin' },
      reason: null,
      spec: { mode: 'uuid_fill', account_ids: [], rollout_percent: 10, chat_compat_uuid: false, header_only_uuid: false, forward_session_headers: 'whitelist' },
    },
    loaded: { rev: 3, loaded_at: '2026-10-23T06:00:00Z' },
    effective: { state: 'masked_by_master', active: false, blockers: [], warnings: [], clipped_fields: [] },
    defaults: { mode: 'off', account_ids: [], rollout_percent: 0, chat_compat_uuid: false, header_only_uuid: false, forward_session_headers: 'whitelist' },
    schema: [
      { field: 'mode', type: 'enum', options: ['off', 'uuid_fill'] },
      { field: 'account_ids', type: 'id_list', ref: 'account' },
      { field: 'rollout_percent', type: 'int', min: 0, max: 100, unit: 'percent' },
      { field: 'chat_compat_uuid', type: 'bool' },
      { field: 'header_only_uuid', type: 'bool' },
      { field: 'forward_session_headers', type: 'enum', options: ['whitelist', 'normalize'] },
    ],
    ...overrides,
  }
}

function notInBuild(key: string, settingKey: string, risk: string, gated: boolean): PolicyEntry {
  return {
    key,
    setting_key: settingKey,
    available: false,
    risk,
    gated_by_master: gated,
    stored: { rev: 0, updated_at: null, updated_by: null, reason: null, spec: {} },
    loaded: null,
    effective: { state: 'not_in_build', active: null, blockers: [], warnings: [], clipped_fields: [] },
    defaults: {},
    schema: [],
  }
}

/** The M1 shape: master and observability implemented, the rest announced as not in the build. */
export function m1Response(options: { master?: boolean; kill?: boolean; identity?: PolicyEntry } = {}): PoliciesResponse {
  return {
    generated_at: '2026-10-23T08:00:05Z',
    process_started_at: '2026-10-23T00:34:14Z',
    build_version: '0.4.7',
    refresh_interval_s: 60,
    static: {
      kill_switch: options.kill ?? false,
      caps: { workers: 1, max_byte_budget_mib: 64, max_rate_mib_per_s: 32 },
    },
    existing: {
      openai_cache_diagnostics_enabled: true,
      openai_cache_aware_routing_enabled: false,
      openai_cache_rollout_percent: 0,
      claude_cache_fallback: { configured: true, group_count: 3, settings_link: '/admin/settings#claude-cache-fallback' },
    },
    policies: [
      masterEntry(options.master ?? false),
      observabilityEntry(),
      notInBuild('northstar', 'cache_northstar_policy', 'low', false),
      options.identity ?? notInBuild('upstream_identity', 'cache_upstream_identity_policy', 'high', true),
      notInBuild('anthropic_identity', 'cache_anthropic_identity_policy', 'high', true),
      notInBuild('routing', 'cache_routing_policy', 'highest', true),
    ],
  }
}

export function validation(overrides: Partial<ValidateResult> = {}): ValidateResult {
  return {
    valid: true,
    errors: [],
    warnings: [],
    diff: [{ field: 'enabled', before: false, after: true }],
    risk: 'low',
    direction: 'enable',
    confirm: { reason_required: false, checkbox_required: false, type_to_confirm: null },
    effective_preview: { state: 'on', active: true, blockers: [] },
    ...overrides,
  }
}
