import type { AvailablePolicy, ConflictOverlap, IdOption, PolicyEntry, PolicySpec, SchemaField } from './types'

export type FieldKind = 'bool' | 'int' | 'enum' | 'enum_list' | 'string' | 'id_list' | 'threshold_rules' | 'unknown'

const KIND_ALIASES: Record<string, FieldKind> = {
  bool: 'bool',
  boolean: 'bool',
  int: 'int',
  integer: 'int',
  number: 'int',
  percent: 'int',
  enum: 'enum',
  enum_list: 'enum_list',
  string_set: 'enum_list',
  string: 'string',
  version: 'string',
  id_list: 'id_list',
  int_list: 'id_list',
  account_ids: 'id_list',
  group_ids: 'id_list',
  threshold_rules: 'threshold_rules',
}

export function fieldKind(field: SchemaField): FieldKind {
  return KIND_ALIASES[field.type] ?? 'unknown'
}

/** String options of an enum or enum_list (`options` is the only key, §7.2). */
export function fieldOptions(field: SchemaField): string[] {
  return (field.options ?? []).filter((o): o is string => typeof o === 'string')
}

/** Named candidates of an account id list (FE-Q3); empty when the server sends none. */
export function idOptions(field: SchemaField): IdOption[] {
  return (field.options ?? []).filter(
    (o): o is IdOption => !!o && typeof o === 'object' && typeof (o as IdOption).id === 'number' && typeof (o as IdOption).name === 'string',
  )
}

/** A key this build ships, with its stored value and defaults present. */
export function isAvailable(entry: PolicyEntry | null | undefined): entry is AvailablePolicy {
  return !!entry && entry.available && !!entry.stored && !!entry.defaults
}

/** `group` / `account` for id lists; inferred from the field name when the schema has no `ref`. */
export function idRef(field: SchemaField): 'group' | 'account' | null {
  if (field.ref === 'group' || field.ref === 'account') return field.ref
  if (field.type === 'group_ids' || /group_ids$/.test(field.field)) return 'group'
  if (field.type === 'account_ids' || /account_ids$/.test(field.field)) return 'account'
  return null
}

/**
 * Warnings that mean the server left a key's candidates out of this answer (m3m4 README §3.4,
 * v1.0.3): a failed lookup, or a disable-direction PUT that skips the account and group tables.
 * An empty `options` then means "not loaded", not "none eligible".
 */
export const CANDIDATE_WARNINGS: readonly string[] = ['account_options_unavailable', 'group_options_unavailable']

/**
 * The field as the card offers it: candidates the server did not load count as absent. The
 * selection stays as stored, ids without a name show as ids, and the server checks what is added.
 */
export function servedField(entry: PolicyEntry, field: SchemaField): SchemaField {
  const unloaded =
    !!idRef(field) && Array.isArray(field.options) && !field.options.length && entry.effective.warnings.some((w) => CANDIDATE_WARNINGS.includes(w))
  return unloaded ? { ...field, options: undefined } : field
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

/**
 * Deep copy of a spec value. Drafts live in reactive refs, so values read from
 * them are Vue proxies, which structuredClone rejects (DataCloneError); specs
 * are plain JSON, so a JSON round trip is exact.
 */
export function cloneValue<T>(value: T): T {
  return value === undefined ? value : (JSON.parse(JSON.stringify(value)) as T)
}

export function stableStringify(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`
  if (isPlainObject(value)) {
    return `{${Object.keys(value)
      .sort()
      .map((k) => `${JSON.stringify(k)}:${stableStringify(value[k])}`)
      .join(',')}}`
  }
  return JSON.stringify(value) ?? 'undefined'
}

export function sameValue(a: unknown, b: unknown): boolean {
  return stableStringify(a) === stableStringify(b)
}

export function getPath(spec: PolicySpec | null | undefined, path: string): unknown {
  let current: unknown = spec
  for (const part of path.split('.')) {
    if (!isPlainObject(current)) return undefined
    current = current[part]
  }
  return current
}

export function setPath(spec: PolicySpec, path: string, value: unknown): PolicySpec {
  const [head, ...rest] = path.split('.')
  if (!rest.length) return { ...spec, [head]: cloneValue(value) }
  const child = isPlainObject(spec[head]) ? (spec[head] as PolicySpec) : {}
  return { ...spec, [head]: setPath(child, rest.join('.'), value) }
}

export interface FieldChange {
  field: SchemaField
  before: unknown
  after: unknown
}

export function changedFields(entry: AvailablePolicy, draft: PolicySpec): FieldChange[] {
  return entry.schema
    .map((field) => ({ field, before: getPath(entry.stored.spec, field.field), after: getPath(draft, field.field) }))
    .filter((c) => !sameValue(c.before, c.after))
}

/**
 * Body `spec` for validate/PUT: omitted fields keep their stored value on the
 * server, so only changed top-level fields are sent; a nested object is sent
 * whole so the merge semantics of nested fields never matter.
 */
export function partialSpec(draft: PolicySpec, changes: FieldChange[]): PolicySpec {
  const spec: PolicySpec = {}
  for (const change of changes) {
    const top = change.field.field.split('.')[0]
    spec[top] = cloneValue(draft[top])
  }
  return spec
}

/** Whether a value moves a feature up (on, wider, higher) or down; null when it cannot be told. */
export function trend(from: unknown, to: unknown): 'up' | 'down' | null {
  if (sameValue(from, to)) return null
  if (typeof from === 'boolean' && typeof to === 'boolean') return to ? 'up' : 'down'
  if (typeof from === 'number' && typeof to === 'number') return to > from ? 'up' : 'down'
  if (typeof from === 'string' && typeof to === 'string') {
    if (to === 'off') return 'down'
    if (from === 'off') return 'up'
  }
  return null
}

export interface MergeResult {
  /** Latest stored value with the admin's clean edits applied; overlapping fields keep the latest value until chosen. */
  draft: PolicySpec
  theirFields: string[]
  merged: string[]
  overlaps: ConflictOverlap[]
}

/**
 * Three-way merge after someone else wrote the key (409, or a refresh while a
 * draft was open). `base` is the entry the admin started editing from. A
 * field only one side changed merges silently; a field both sides changed to
 * different values becomes an overlap that the admin must decide (M-3).
 */
export function mergeDraft(base: AvailablePolicy, draft: PolicySpec, latest: AvailablePolicy): MergeResult {
  const theirFields = latest.schema
    .filter((f) => !sameValue(getPath(base.stored.spec, f.field), getPath(latest.stored.spec, f.field)))
    .map((f) => f.field)
  let next = cloneValue(latest.stored.spec)
  const merged: string[] = []
  const overlaps: ConflictOverlap[] = []
  for (const change of changedFields(base, draft)) {
    const path = change.field.field
    if (!latest.schema.some((f) => f.field === path)) continue
    const theirs = getPath(latest.stored.spec, path)
    if (!theirFields.includes(path)) {
      next = setPath(next, path, change.after)
      merged.push(path)
    } else if (!sameValue(theirs, change.after)) {
      const reverting = trend(change.before, theirs) === 'down' && trend(theirs, change.after) === 'up'
      overlaps.push({ field: path, base: change.before, mine: change.after, theirs, choice: null, reverting })
    }
  }
  return { draft: next, theirFields, merged, overlaps }
}

/** Applies the admin's per-field decisions to a merged draft. */
export function applyChoices(draft: PolicySpec, overlaps: ConflictOverlap[]): PolicySpec {
  let next = draft
  for (const o of overlaps) next = setPath(next, o.field, o.choice === 'mine' ? o.mine : o.theirs)
  return next
}

export interface InlineIssue {
  field: string
  level: 'error' | 'warning'
  code: string
  params?: Record<string, string | number>
}

/**
 * Immediate feedback while editing. The server's validate call is the
 * authority; these checks only mirror the schema ranges and the hard rules of
 * DESIGN §11.3 so a wrong value is visible before the preview.
 */
export function inlineIssues(entry: AvailablePolicy, draft: PolicySpec, baseline: PolicySpec = entry.stored.spec): InlineIssue[] {
  const issues: InlineIssue[] = []
  const has = (path: string) => entry.schema.some((f) => f.field === path)
  const get = (path: string) => getPath(draft, path)

  for (const field of entry.schema) {
    const value = get(field.field)
    // Only edits are checked: an untouched value above a lowered cap is shown as "clipped" and never blocks other edits (C-10).
    if (sameValue(getPath(baseline, field.field), value)) continue
    const kind = fieldKind(field)
    if (kind === 'int') {
      if (typeof value !== 'number' || !Number.isInteger(value)) issues.push({ field: field.field, level: 'error', code: 'not_integer' })
      else if ((field.min !== undefined && value < field.min) || (field.max !== undefined && value > field.max)) {
        const code = field.cap_source && field.max !== undefined && value > field.max ? 'exceeds_static_cap' : 'out_of_range'
        issues.push({ field: field.field, level: 'error', code, params: { min: field.min ?? '-', max: field.max ?? '-', limit: field.max ?? '-' } })
      }
    }
    if (kind === 'id_list') {
      const ok = Array.isArray(value) && value.every((v) => Number.isSafeInteger(v) && (v as number) > 0)
      if (!ok) issues.push({ field: field.field, level: 'error', code: 'ids' })
      else if (new Set(value as number[]).size !== (value as number[]).length) issues.push({ field: field.field, level: 'error', code: 'duplicate' })
    }
    if (kind === 'enum') {
      const options = fieldOptions(field)
      if (options.length && !options.includes(String(value))) issues.push({ field: field.field, level: 'warning', code: 'unknown_option', params: { value: String(value) } })
    }
  }

  if (entry.key === 'observability' && has('ingress_chain_percent') && has('chain_percent')) {
    const ingress = get('ingress_chain_percent')
    const chain = get('chain_percent')
    if (typeof ingress === 'number' && typeof chain === 'number' && ingress > chain) {
      issues.push({ field: 'ingress_chain_percent', level: 'error', code: 'ingress_above_chain', params: { chain } })
    }
  }
  if (entry.key === 'upstream_identity') {
    const uuidFill = get('mode') === 'uuid_fill'
    for (const sub of ['chat_compat_uuid', 'header_only_uuid']) {
      if (has(sub) && get(sub) === true && !uuidFill) issues.push({ field: sub, level: 'error', code: 'requires_mode_uuid_fill' })
    }
  }
  if (entry.key === 'anthropic_identity' && has('metadata_group_ids')) {
    const anchors = Array.isArray(get('anchor_group_ids')) ? (get('anchor_group_ids') as unknown[]) : []
    const metadata = Array.isArray(get('metadata_group_ids')) ? (get('metadata_group_ids') as unknown[]) : []
    if (metadata.some((id) => !anchors.includes(id))) issues.push({ field: 'metadata_group_ids', level: 'error', code: 'not_subset_of_anchor_groups' })
  }
  if (entry.key === 'northstar' && has('policy_version')) {
    const ruleChanged = entry.schema.some(
      (f) => f.field !== 'policy_version' && !sameValue(getPath(baseline, f.field), get(f.field)) && !['rollup_enabled', 'backfill_days'].includes(f.field),
    )
    if (ruleChanged && sameValue(getPath(baseline, 'policy_version'), get('policy_version'))) {
      issues.push({ field: 'policy_version', level: 'error', code: 'version_must_change' })
    }
  }
  return issues
}

/**
 * Controls that cannot be switched on yet because a hard prerequisite is
 * missing. A field that already deviates stays editable so it can be fixed.
 */
export function lockReason(entry: AvailablePolicy, draft: PolicySpec, field: SchemaField): string | null {
  if (entry.key !== 'upstream_identity') return null
  if (!['chat_compat_uuid', 'header_only_uuid'].includes(field.field)) return null
  if (getPath(draft, field.field) === true) return null
  return getPath(draft, 'mode') === 'uuid_fill' ? null : 'requires_mode_uuid_fill'
}

/** Whether a field holds something other than its default (used for "overridden by master" marks). */
export function isConfigured(entry: AvailablePolicy, draft: PolicySpec, field: SchemaField): boolean {
  return !sameValue(getPath(entry.defaults, field.field), getPath(draft, field.field))
}
