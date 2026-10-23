import { describe, expect, it } from 'vitest'
import { applyChoices, changedFields, fieldKind, fieldOptions, getPath, idOptions, inlineIssues, lockReason, mergeDraft, partialSpec, setPath, trend } from '../schema'
import type { AvailablePolicy } from '../types'
import { identityEntry, observabilityEntry, rewritten } from './fixtures'

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
    const entry: AvailablePolicy = {
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
    expect(inlineIssues(entry, setPath(entry.stored.spec, 'chain_percent', -1))[0]).toMatchObject({ code: 'out_of_range', params: { min: 0, max: 100 } })
    // Untouched values are never flagged, so a lowered cap cannot block other edits (C-10).
    const clipped = { ...entry, stored: { ...entry.stored, spec: { ...entry.stored.spec, byte_budget_mib: 128 } } }
    expect(inlineIssues(clipped, setPath(clipped.stored.spec, 'chain_percent', 50))).toEqual([])
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

    const anthropic: AvailablePolicy = {
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
    const entry: AvailablePolicy = {
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

  it('merges three ways: one-sided edits merge, clashes wait for a choice and flag a reverted rollback', () => {
    const base = identityEntry()
    let draft = setPath(base.stored.spec, 'rollout_percent', 60)
    draft = setPath(draft, 'forward_session_headers', 'normalize')
    draft = setPath(draft, 'chat_compat_uuid', true)
    const latest = rewritten(base, 5, { rollout_percent: 0, header_only_uuid: true, chat_compat_uuid: true })
    const merge = mergeDraft(base, draft, latest)
    expect(merge.theirFields).toEqual(['rollout_percent', 'chat_compat_uuid', 'header_only_uuid'])
    // chat_compat_uuid: both made the same change, so nothing to decide.
    expect(merge.merged).toEqual(['forward_session_headers'])
    expect(merge.overlaps).toEqual([{ field: 'rollout_percent', base: 50, mine: 60, theirs: 0, choice: null, reverting: true }])
    expect(merge.draft).toMatchObject({ rollout_percent: 0, header_only_uuid: true, forward_session_headers: 'normalize' })
    expect(applyChoices(merge.draft, [{ ...merge.overlaps[0], choice: 'mine' }])).toMatchObject({ rollout_percent: 60 })
    expect(applyChoices(merge.draft, [{ ...merge.overlaps[0], choice: 'theirs' }])).toMatchObject({ rollout_percent: 0 })
  })

  it('tells turning up from turning down', () => {
    expect([trend(false, true), trend(true, false), trend(10, 0), trend(0, 10), trend('off', 'uuid_fill'), trend('uuid_fill', 'off'), trend('whitelist', 'normalize'), trend(1, 1)]).toEqual([
      'up',
      'down',
      'down',
      'up',
      'up',
      'down',
      null,
      null,
    ])
  })

  it('reads string options and named account candidates from the schema', () => {
    const fields = identityEntry().schema
    expect(fieldOptions(fields.find((f) => f.field === 'mode')!)).toEqual(['off', 'uuid_fill'])
    expect(fieldOptions(fields.find((f) => f.field === 'account_ids')!)).toEqual([])
    expect(idOptions(fields.find((f) => f.field === 'account_ids')!)[0]).toEqual({ id: 101, name: 'relay-a-01', platform: 'openai' })
  })
})
