import type { CollaborationSettings } from '@/api/admin/resilience'

export const COLLAB_BOOLEAN_FIELDS = [
  'incremental_declarations', 'records', 'tree_affinity', 'ladder', 'lossless_first', 'record_routing', 'strict_marker_veto'
] as const

export const COLLAB_LOCAL_ENDING_MODES = ['off', 'groups', 'all'] as const

export function collaborationDefaults(): CollaborationSettings {
  return {
    incremental_declarations: false,
    records: true,
    tree_affinity: false,
    ladder: false,
    lossless_first: false,
    local_ending: { mode: 'off', groups: [] },
    record_routing: false,
    strict_marker_veto: false
  }
}

// Missing fields use release defaults, including documents saved by an older server.
// This is a display projection and edit validator, never a serialized server document.
export function readCollaborationSettings(value: unknown): CollaborationSettings | null {
  const result = collaborationDefaults()
  if (value === undefined) return result
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const section = value as Record<string, unknown>
  for (const key of COLLAB_BOOLEAN_FIELDS) {
    if (section[key] === undefined) continue
    if (typeof section[key] !== 'boolean') return null
    result[key] = section[key]
  }
  if (section.local_ending !== undefined) {
    if (!section.local_ending || typeof section.local_ending !== 'object' || Array.isArray(section.local_ending)) return null
    const ending = section.local_ending as Record<string, unknown>
    if (ending.mode !== undefined) {
      if (!COLLAB_LOCAL_ENDING_MODES.some((mode) => mode === ending.mode)) return null
      result.local_ending.mode = ending.mode as CollaborationSettings['local_ending']['mode']
    }
    if (ending.groups !== undefined) {
      if (!Array.isArray(ending.groups) || ending.groups.some((id) => !Number.isSafeInteger(id) || id <= 0)) return null
      result.local_ending.groups = [...new Set(ending.groups as number[])]
    }
  }
  return result
}

// Known controls patch only fields the administrator changed. Do not add defaults
// the server did not return, or discard extensions understood by a newer backend.
export function patchCollaborationSettings(raw: unknown, next: CollaborationSettings): Record<string, unknown> {
  const previous = readCollaborationSettings(raw) ?? collaborationDefaults()
  const result = { ...asObject(raw) }
  for (const key of COLLAB_BOOLEAN_FIELDS) {
    if (next[key] !== previous[key]) result[key] = next[key]
  }
  const ending = { ...asObject(result.local_ending) }
  for (const key of ['mode', 'groups'] as const) {
    if (!sameValue(next.local_ending[key], previous.local_ending[key])) ending[key] = next.local_ending[key]
  }
  if (Object.keys(ending).length) result.local_ending = ending
  return result
}

// JSON edits may echo opaque fields received from this server, but cannot invent
// or alter them: older backends reject unknown fields rather than ignoring them.
export function validCollaborationEdit(value: unknown, serverValue: unknown): boolean {
  if (!readCollaborationSettings(value)) return false
  const section = asObject(value)
  const server = asObject(serverValue)
  const known = new Set<string>([...COLLAB_BOOLEAN_FIELDS, 'local_ending'])
  for (const key of Object.keys(section)) {
    if (!known.has(key) && (!Object.prototype.hasOwnProperty.call(server, key) || !sameValue(section[key], server[key]))) return false
  }
  const ending = asObject(section.local_ending)
  const serverEnding = asObject(server.local_ending)
  for (const key of Object.keys(ending)) {
    if (key !== 'mode' && key !== 'groups' && (!Object.prototype.hasOwnProperty.call(serverEnding, key) || !sameValue(ending[key], serverEnding[key]))) return false
  }
  return true
}

export function mergeCollaborationEdit(current: unknown, edit: unknown): Record<string, unknown> {
  const result = { ...asObject(current), ...asObject(edit) }
  if (asObject(edit).local_ending !== undefined) {
    result.local_ending = { ...asObject(asObject(current).local_ending), ...asObject(asObject(edit).local_ending) }
  }
  return result
}

function asObject(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

export function sameValue(left: unknown, right: unknown): boolean {
  if (left === right) return true
  if (Array.isArray(left) && Array.isArray(right)) {
    return left.length === right.length && left.every((value, index) => sameValue(value, right[index]))
  }
  if (left && right && typeof left === 'object' && typeof right === 'object' && !Array.isArray(left) && !Array.isArray(right)) {
    const a = asObject(left), b = asObject(right)
    return Object.keys(a).length === Object.keys(b).length && Object.keys(a).every((key) => Object.prototype.hasOwnProperty.call(b, key) && sameValue(a[key], b[key]))
  }
  return false
}
