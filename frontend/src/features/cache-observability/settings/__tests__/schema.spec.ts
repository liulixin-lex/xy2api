import { describe, expect, it } from 'vitest'
import { changedFields, fieldKind, getPath, inlineIssues, lockReason, partialSpec, rebaseDraft, setPath } from '../schema'
import type { PolicyEntry } from '../types'
import { identityEntry, observabilityEntry } from './fixtures'

describe('policy schema helpers', () => {
  it('reads and writes nested field paths without mutating the source', () => {
    const spec = { keep_sticky_on_busy: { enabled: false, group_ids: [1] }, affinity_mode: 'score' }
    const next = setPath(spec, 'keep_sticky_on_busy.enabled', true)
    expect(getPath(next, 'keep_sticky_on_busy.enabled')).toBe(true)
    expect(getPath(next, 'keep_sticky_on_busy.group_ids')).toEqual([1])
    expect(spec.keep_sticky_on_busy.enabled).toBe(false)
    expect(getPath(spec, 'missing.path')).toBeUndefined()
  })

  it('maps schema types and treats unknown ones as read-only', () => {
    expect(fieldKind({ field: 'a', type: 'bool' })).toBe('bool')
    expect(fieldKind({ field: 'a', type: 'integer' })).toBe('int')
    expect(fieldKind({ field: 'a', type: 'group_ids' })).toBe('id_list')
    expect(fieldKind({ field: 'a', type: 'json_blob' })).toBe('unknown')
  })

  it('sends only changed top-level fields, nested objects whole', () => {
    const entry: PolicyEntry = {
      ...observabilityEntry(),
      key: 'routing',
      stored: { ...observabilityEntry().stored, spec: { keep_sticky_on_busy: { enabled: false, rollout_percent: 0 }, affinity_mode: 'score' } },
      schema: [
        { field: 'keep_sticky_on_busy.enabled', type: 'bool' },
        { field: 'keep_sticky_on_busy.rollout_percent', type: 'int', min: 0, max: 100 },
        { field: 'affinity_mode', type: 'enum', options: ['score', 'host'] },
      ],
    }
    const draft = setPath(entry.stored.spec, 'keep_sticky_on_busy.enabled', true)
    const changes = changedFields(entry, draft)
    expect(changes.map((c) => c.field.field)).toEqual(['keep_sticky_on_busy.enabled'])
    expect(partialSpec(draft, changes)).toEqual({ keep_sticky_on_busy: { enabled: true, rollout_percent: 0 } })
  })

  it('checks schema ranges and static caps inline', () => {
    const entry = observabilityEntry()
    let draft = setPath(entry.stored.spec, 'byte_budget_mib', 128)
    draft = setPath(draft, 'sample_percent', 12.5)
    const codes = inlineIssues(entry, draft).map((i) => `${i.field}:${i.code}`)
    expect(codes).toEqual(expect.arrayContaining(['byte_budget_mib:exceeds_static_cap', 'sample_percent:not_integer']))
    expect(inlineIssues(entry, setPath(entry.stored.spec, 'chain_percent', -1))[0]).toMatchObject({ code: 'out_of_range' })
  })

  it('mirrors the hard rules of DESIGN §11.3 and locks dependent switches', () => {
    const entry = identityEntry()
    const off = setPath(entry.stored.spec, 'mode', 'off')
    const bad = setPath(off, 'chat_compat_uuid', true)
    expect(inlineIssues(entry, bad).find((i) => i.field === 'chat_compat_uuid')?.code).toBe('requires_mode_uuid_fill')
    const header = entry.schema.find((f) => f.field === 'header_only_uuid')!
    expect(lockReason(entry, off, header)).toBe('requires_mode_uuid_fill')
    expect(lockReason(entry, entry.stored.spec, header)).toBeNull()
    // forward_session_headers does not depend on the mode (L-02).
    expect(lockReason(entry, off, entry.schema.find((f) => f.field === 'forward_session_headers')!)).toBeNull()

    const anthropic: PolicyEntry = {
      ...entry,
      key: 'anthropic_identity',
      stored: { ...entry.stored, spec: { anchor_group_ids: [1], metadata_group_ids: [] } },
      schema: [
        { field: 'anchor_group_ids', type: 'group_ids' },
        { field: 'metadata_group_ids', type: 'group_ids' },
      ],
    }
    const notSubset = setPath(anthropic.stored.spec, 'metadata_group_ids', [2])
    expect(inlineIssues(anthropic, notSubset)[0]).toMatchObject({ field: 'metadata_group_ids', code: 'not_subset_of_anchor_groups' })
  })

  it('requires a new north-star version when a rule changes, but not for the job switches', () => {
    const entry: PolicyEntry = {
      ...observabilityEntry(),
      key: 'northstar',
      stored: { ...observabilityEntry().stored, spec: { rollup_enabled: false, policy_version: '2026-10-22-v1', exclude_media: true } },
      schema: [
        { field: 'rollup_enabled', type: 'bool' },
        { field: 'policy_version', type: 'string' },
        { field: 'exclude_media', type: 'bool' },
      ],
    }
    expect(inlineIssues(entry, setPath(entry.stored.spec, 'rollup_enabled', true))).toEqual([])
    const rule = setPath(entry.stored.spec, 'exclude_media', false)
    expect(inlineIssues(entry, rule)[0]).toMatchObject({ field: 'policy_version', code: 'version_must_change' })
    expect(inlineIssues(entry, setPath(rule, 'policy_version', '2026-10-24-v1'))).toEqual([])
  })

  it('re-applies only the admin edits on top of a newer stored value after a conflict', () => {
    const old = identityEntry()
    const draft = setPath(old.stored.spec, 'rollout_percent', 20)
    const fresh = identityEntry({ stored: { ...old.stored, rev: 4, spec: { ...old.stored.spec, forward_session_headers: 'normalize' } } })
    const rebased = rebaseDraft(old, draft, fresh)
    expect(rebased).toMatchObject({ rollout_percent: 20, forward_session_headers: 'normalize' })
  })
})
