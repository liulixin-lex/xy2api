// Response and request shapes of /api/v1/admin/cache/policies (CACHE_PANEL_API §7
// and DESIGN §11.7, v1.7.4). Ranges, enums and units always come from the
// per-key `schema`; the panel never hard-codes them.

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

/** Candidate of an `id_list` with `ref=account` or `ref=group` (FE-Q3): only what the server deems eligible. */
export interface IdOption {
  id: number
  name: string
  platform?: string
}

export interface SchemaField {
  field: string
  type: string
  min?: number
  max?: number
  /** `percent`, `MiB`, `MiB/s` or `days`; an unknown unit is shown as is. */
  unit?: string
  /** Strings for enum/enum_list, integers for threshold_rules, IdOption for account id lists. */
  options?: (string | number | IdOption)[] | null
  ref?: string
  cap_source?: string
}

export interface PolicyEntry {
  key: PolicyKey | string
  setting_key: string
  available: boolean
  risk: 'low' | 'high' | 'highest' | string
  gated_by_master: boolean
  /** null, like `loaded` and `defaults`, for a key this build does not ship. */
  stored: StoredPolicy | null
  loaded: LoadedPolicy | null
  effective: PolicyEffective
  defaults: PolicySpec | null
  schema: SchemaField[]
}

/** An entry this build ships: stored value and defaults are present. */
export interface AvailablePolicy extends PolicyEntry {
  stored: StoredPolicy
  defaults: PolicySpec
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
  /** Dotted spec path; `reason` for the change reason, empty for a malformed body. */
  field: string
  code: string
  message?: string
  limit?: { min?: number; max?: number }
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
  /** Stored rev right now; `rev_stale` means the draft's expected_rev is behind it (go to the merge). */
  current_rev: number
  rev_stale: boolean
  effective_preview: { state: string; active: boolean | null; blockers: string[] } | null
  /** Keys whose effective state changes on submit: the three gated keys for master, the key itself otherwise. */
  affected: AffectedKey[]
}

export interface AffectedKey {
  key: string
  state_before: string
  state_after: string
  active_after: boolean | null
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

/** Why `history_complete` is false (§7.6, v1.7.4.6); null when it is true. */
export type HistoryIncompleteReason = 'earlier_missing' | 'gap'

export interface HistoryResponse {
  items: HistoryItem[]
  history_complete: boolean
  history_incomplete_reason: HistoryIncompleteReason | null
}

export type WriteFailure =
  | { kind: 'conflict'; currentRev: number | null }
  /** `errors` is empty when the server sent none or `metadata.errors` could not be parsed. */
  | { kind: 'invalid'; errors: FieldError[] }
  /** `missing` ⊆ reason, checkbox, typed, recomputed by the server on PUT. */
  | { kind: 'confirm'; missing: string[]; typeToConfirm: string | null }
  | { kind: 'not_in_build' }
  | { kind: 'failed'; message: string | null }

/**
 * A write that landed between the admin's base snapshot and the latest stored
 * value (after a 409, or found on a refresh while a draft was open).
 */
export interface ConflictOverlap {
  field: string
  base: unknown
  mine: unknown
  theirs: unknown
  choice: 'mine' | 'theirs' | null
  /** The other write turned this down or off and the admin's value would turn it back up. */
  reverting: boolean
}

/**
 * Writes that landed between the admin's base snapshot and the latest stored
 * value (a 409, or `rev_stale` from validate), merged three ways (DESIGN §11.7).
 */
export interface PolicyConflict {
  baseRev: number
  latestRev: number
  /** Envelope of the latest write; shown when the history cannot be read. */
  latest: { updated_at: string | null; updated_by: PolicyActor | null; reason: string | null }
  /** Successful writes after the base rev, newest first; null while loading or when unreadable. */
  writes: HistoryItem[] | null
  /** Fields the other write(s) changed, base to latest. */
  theirFields: string[]
  /** The admin's own edits that applied cleanly on top of the latest value. */
  merged: string[]
  /** Fields both sides changed to different values; each needs an explicit choice. */
  overlaps: ConflictOverlap[]
}
