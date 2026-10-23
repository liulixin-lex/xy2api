// Response and request shapes of /api/v1/admin/cache/policies (CACHE_PANEL_API §7,
// DESIGN §11, v1.6). Ranges, enums and units always come from the per-key
// `schema`; the panel never hard-codes them.

export type PolicyKey = 'master' | 'observability' | 'northstar' | 'upstream_identity' | 'anthropic_identity' | 'routing'

export type PolicySpec = Record<string, unknown>

export interface PolicyActor {
  user_id: number | null
  email: string | null
}

export interface StoredPolicy {
  rev: number
  updated_at: string | null
  updated_by: PolicyActor | null
  reason: string | null
  spec: PolicySpec
}

export interface LoadedPolicy {
  rev: number
  loaded_at: string | null
}

export type EffectiveState =
  | 'off'
  | 'on'
  | 'masked_by_master'
  | 'killed'
  | 'blocked'
  | 'pending_reload'
  | 'parse_error'
  | 'not_in_build'

export interface PolicyEffective {
  state: EffectiveState | string
  active: boolean | null
  blockers: string[]
  warnings: string[]
  clipped_fields: string[]
}

export interface SchemaField {
  field: string
  type: string
  min?: number
  max?: number
  unit?: string
  options?: string[]
  enum?: string[]
  ref?: string
  cap_source?: string
}

export interface PolicyEntry {
  key: PolicyKey | string
  setting_key: string
  available: boolean
  risk: 'low' | 'high' | 'highest' | string
  gated_by_master: boolean
  stored: StoredPolicy
  loaded: LoadedPolicy | null
  effective: PolicyEffective
  defaults: PolicySpec
  schema: SchemaField[]
}

export interface ExistingMechanisms {
  openai_cache_diagnostics_enabled?: boolean | null
  openai_cache_aware_routing_enabled?: boolean | null
  openai_cache_rollout_percent?: number | null
  claude_cache_fallback?: { configured: boolean; group_count: number; settings_link?: string } | null
}

export interface PoliciesResponse {
  generated_at: string
  process_started_at: string | null
  build_version: string | null
  refresh_interval_s: number
  static: {
    kill_switch: boolean
    caps: Record<string, number>
  }
  existing: ExistingMechanisms
  policies: PolicyEntry[]
}

export interface FieldError {
  field: string
  code: string
  message?: string
  limit?: number
}

export interface ConfirmRules {
  reason_required: boolean
  checkbox_required: boolean
  type_to_confirm: string | null
}

export interface ValidateResult {
  valid: boolean
  errors: FieldError[]
  warnings: string[]
  diff: { field: string; before: unknown; after: unknown }[]
  risk: string
  direction: 'disable' | 'enable' | 'mixed' | string
  confirm: ConfirmRules
  effective_preview: { state: string; active: boolean | null; blockers: string[] } | null
}

export interface WriteBody {
  expected_rev: number
  spec: PolicySpec
  reason?: string
  confirm?: { checkbox: boolean; typed: string | null }
}

export interface HistoryItem {
  at: string
  key: string
  action: string
  actor: PolicyActor | null
  status_code: number
  rev_before: number | null
  rev_after: number | null
  changed_fields: string[]
  reason: string | null
}

export interface HistoryResponse {
  items: HistoryItem[]
  history_complete: boolean
}

export type WriteFailure =
  | { kind: 'conflict'; currentRev: number | null }
  | { kind: 'invalid'; errors: FieldError[] }
  | { kind: 'confirm' }
  | { kind: 'not_in_build' }
  | { kind: 'failed'; message: string | null }
