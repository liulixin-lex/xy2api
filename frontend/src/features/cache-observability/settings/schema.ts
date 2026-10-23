import type { PolicyEntry, PolicySpec, SchemaField } from './types'

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

export function fieldOptions(field: SchemaField): string[] {
  return field.options ?? field.enum ?? []
}

/** `group` / `account` for id lists; inferred from the field name when the schema has no `ref`. */
export function idRef(field: SchemaField): 'group' | 'account' | null {
  if (field.ref === 'group' || field.ref === 'account') return field.ref
  if (field.type === 'group_ids' || /group_ids$/.test(field.field)) return 'group'
  if (field.type === 'account_ids' || /account_ids$/.test(field.field)) return 'account'
  return null
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
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
  if (!rest.length) return { ...spec, [head]: structuredClone(value) }
  const child = isPlainObject(spec[head]) ? (spec[head] as PolicySpec) : {}
  return { ...spec, [head]: setPath(child, rest.join('.'), value) }
}

export interface FieldChange {
  field: SchemaField
  before: unknown
  after: unknown
}

export function changedFields(entry: PolicyEntry, draft: PolicySpec): FieldChange[] {
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
    spec[top] = structuredClone(draft[top])
  }
  return spec
}

/** Re-applies the admin's edits on top of a newer stored value after a 409. */
export function rebaseDraft(oldEntry: PolicyEntry, oldDraft: PolicySpec, freshEntry: PolicyEntry): PolicySpec {
  let draft = structuredClone(freshEntry.stored.spec)
  for (const change of changedFields(oldEntry, oldDraft)) {
    if (freshEntry.schema.some((f) => f.field === change.field.field)) draft = setPath(draft, change.field.field, change.after)
  }
  return draft
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
export function inlineIssues(entry: PolicyEntry, draft: PolicySpec, baseline: PolicySpec = entry.stored.spec): InlineIssue[] {
  const issues: InlineIssue[] = []
  const has = (path: string) => entry.schema.some((f) => f.field === path)
  const get = (path: string) => getPath(draft, path)

  for (const field of entry.schema) {
    const value = get(field.field)
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
export function lockReason(entry: PolicyEntry, draft: PolicySpec, field: SchemaField): string | null {
  if (entry.key !== 'upstream_identity') return null
  if (!['chat_compat_uuid', 'header_only_uuid'].includes(field.field)) return null
  if (getPath(draft, field.field) === true) return null
  return getPath(draft, 'mode') === 'uuid_fill' ? null : 'requires_mode_uuid_fill'
}

/** Whether a field holds something other than its default (used for "overridden by master" marks). */
export function isConfigured(entry: PolicyEntry, draft: PolicySpec, field: SchemaField): boolean {
  return !sameValue(getPath(entry.defaults, field.field), getPath(draft, field.field))
}
