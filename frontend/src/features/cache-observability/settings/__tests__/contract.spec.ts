// API-layer contract tests (REVIEW_F-1a §2, C-1 to C-13; C-14 is in the panel spec).
// The real apiClient runs with a stub adapter that answers with F's samples,
// so the envelope unwrapping and error rejection are the production ones.
import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { InternalAxiosRequestConfig } from 'axios'

vi.mock('@/i18n', () => ({ getLocale: () => 'zh-CN' }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {} } }))

import { apiClient } from '@/api/client'
import { listHistory, listPolicies, policyApi, putPolicy, toWriteFailure, validatePolicy } from '../api'
import { errorParams } from '../format'
import { fieldKind, idOptions, isAvailable } from '../schema'
import { usePolicies } from '../usePolicies'
import type { AvailablePolicy, PolicyEntry } from '../types'
import { entryOf, list, rejection, sample, SAMPLE_NAMES, type ContractSample } from './contract'
import { identityEntry, rewritten } from './fixtures'

interface Call {
  method: string
  url: string
  params: Record<string, unknown> | undefined
  data: unknown
}

let calls: Call[] = []
let queue: ContractSample[] = []

function respond(...samples: ContractSample[]) {
  queue.push(...samples)
}

/** A success envelope around a value derived from a sample (only values differ, never the shape). */
function ok(data: unknown): ContractSample {
  return { _contract: [], _note: 'variation', request: { method: 'GET', path: '', body: null }, status: 200, response: { code: 0, message: 'success', data } }
}

beforeEach(() => {
  calls = []
  queue = []
  apiClient.defaults.adapter = vi.fn(async (config: InternalAxiosRequestConfig) => {
    calls.push({
      method: String(config.method).toUpperCase(),
      url: String(config.url),
      params: config.params,
      data: typeof config.data === 'string' ? JSON.parse(config.data) : config.data,
    })
    const next = queue.shift()
    if (!next) throw new Error(`unexpected request ${config.method} ${config.url}`)
    if (next.status < 400) return { status: next.status, statusText: 'OK', headers: {}, config, data: next.response }
    throw { response: { status: next.status, data: next.response, headers: {} }, config, code: 'ERR_BAD_REQUEST' }
  })
})

function setup(options: { now?: string; groups?: unknown[] } = {}) {
  const now = new Date(options.now ?? '2026-10-23T09:00:00Z')
  return usePolicies({ api: policyApi, loadGroups: async () => (options.groups ?? []) as never, now: () => now })
}

async function rejected(promise: Promise<unknown>): Promise<unknown> {
  try {
    await promise
  } catch (error) {
    return error
  }
  throw new Error('expected a rejection')
}

describe('cache policies contract v1.7.4', () => {
  it('rejects every error sample the way the unit tests assume', async () => {
    for (const name of ['put_400', 'put_403', 'put_404', 'put_409', 'put_422']) {
      respond(sample(name))
      expect(await rejected(apiClient.put('/admin/cache/policies/observability', {})), name).toEqual(rejection(name))
    }
  })

  it('C-1 lists all six keys, all off by default, and leaves keys outside the build without values', async () => {
    respond(sample('list_default_all_off'))
    const response = await listPolicies()
    expect(calls[0]).toMatchObject({ method: 'GET', url: '/admin/cache/policies' })
    expect(response.policies.map((p) => p.key)).toEqual(['master', 'observability', 'northstar', 'upstream_identity', 'anthropic_identity', 'routing'])
    expect(Object.fromEntries(response.policies.map((p) => [p.key, p.gated_by_master]))).toEqual({
      master: false,
      observability: false,
      northstar: false,
      upstream_identity: true,
      anthropic_identity: true,
      routing: true,
    })
    for (const p of response.policies) {
      if (p.available) {
        expect(isAvailable(p), p.key).toBe(true)
        expect(p.stored).toMatchObject({ rev: 0, updated_at: null, updated_by: null, reason: null })
        expect(p.stored?.spec).toEqual(p.defaults)
        expect(p.effective.state).toBe('off')
      } else {
        expect(isAvailable(p), p.key).toBe(false)
        expect([p.stored, p.loaded, p.defaults]).toEqual([null, null, null])
        expect(p.schema).toEqual([])
        expect(p.effective.state).toBe('not_in_build')
      }
    }

    respond(sample('list_default_all_off'))
    const state = setup()
    await state.load()
    expect(Object.keys(state.drafts.value).sort()).toEqual(['master', 'observability'])
    expect(state.stateOf('routing')).toBe('not_in_build')
    expect(state.dirtyKeys.value).toEqual([])
  })

  it('C-2 keeps to the schema vocabulary', () => {
    const units = new Set(['percent', 'MiB', 'MiB/s', 'days'])
    for (const name of SAMPLE_NAMES.filter((n) => n.startsWith('list_'))) {
      for (const p of list(name).policies) {
        for (const field of p.schema) {
          const where = `${name} ${p.key}.${field.field}`
          expect(fieldKind(field), where).not.toBe('unknown')
          expect('enum' in field, where).toBe(false)
          if (field.unit !== undefined) expect(units.has(field.unit), where).toBe(true)
          if (['enum', 'enum_list'].includes(fieldKind(field))) expect(field.options?.every((o) => typeof o === 'string'), where).toBe(true)
          if (fieldKind(field) === 'int') expect([typeof field.min, typeof field.max], where).toEqual(['number', 'number'])
          if (fieldKind(field) === 'id_list') expect(['group', 'account']).toContain(field.ref)
          if (field.cap_source) expect(field.cap_source, where).toMatch(/^gateway\./)
        }
      }
    }
    const accounts = identityEntry().schema.find((f) => f.field === 'account_ids')!
    expect(idOptions(accounts).map((o) => o.name)).toEqual(['relay-a-01', 'relay-b-01', 'relay-c-01'])
  })

  it('C-3 answers a failed validation with 200 and valid=false, limits included', async () => {
    respond(sample('list_default_all_off'))
    const state = setup()
    await state.load()
    state.update('observability', 'sample_percent', 150)
    respond(sample('validate_invalid'))
    await state.openPreview('observability')
    expect(calls[1]).toMatchObject({
      method: 'POST',
      url: '/admin/cache/policies/observability/validate',
      data: { expected_rev: 0, spec: { sample_percent: 150 } },
    })
    const result = state.preview.value?.result
    expect(result?.valid).toBe(false)
    expect(result?.errors.map((e) => e.code)).toEqual(['out_of_range', 'unknown_field', 'too_long'])
    const field = (entryOf('list_default_all_off', 'observability') as AvailablePolicy).schema.find((f) => f.field === 'sample_percent')
    expect(errorParams(field, result!.errors[0])).toEqual({ min: 0, max: 100, limit: 100 })
    expect(errorParams(undefined, result!.errors[2])).toMatchObject({ max: 200 })
  })

  it('C-4 asks for the confirmation each direction and risk needs, and reads what a 422 still misses', async () => {
    const confirms: Record<string, unknown> = {}
    for (const name of ['validate_ok_disable', 'validate_ok_observability_enable', 'validate_ok_master_enable_m3', 'validate_ok_routing_enable_m4']) {
      respond(sample(name))
      confirms[name] = (await validatePolicy('observability', { expected_rev: 0, spec: {} })).confirm
    }
    expect(confirms.validate_ok_disable).toEqual({ reason_required: false, checkbox_required: false, type_to_confirm: null })
    expect(confirms.validate_ok_observability_enable).toEqual({ reason_required: false, checkbox_required: false, type_to_confirm: null })
    expect(confirms.validate_ok_master_enable_m3).toEqual({ reason_required: true, checkbox_required: true, type_to_confirm: null })
    expect(confirms.validate_ok_routing_enable_m4).toMatchObject({ reason_required: true, checkbox_required: true, type_to_confirm: 'cache_routing_policy' })

    const unconfirmed = sample('put_422')
    respond(unconfirmed)
    const failure = toWriteFailure(await rejected(putPolicy('routing', unconfirmed.request.body as Parameters<typeof putPolicy>[1])))
    expect(failure).toEqual({ kind: 'confirm', missing: ['reason', 'checkbox', 'typed'], typeToConfirm: 'cache_routing_policy' })
  })

  it('C-5 reads current_rev from a 409 and fetches only the writes after expected_rev', async () => {
    respond(sample('put_409'))
    expect(toWriteFailure(await rejected(putPolicy('upstream_identity', { expected_rev: 4, spec: { rollout_percent: 60 } })))).toEqual({
      kind: 'conflict',
      currentRev: 5,
    })
    const between = sample('history_after_rev')
    respond(between)
    const history = await listHistory('upstream_identity', 20, 4)
    const query = new URLSearchParams(between.request.path.split('?')[1])
    expect(calls[1]).toMatchObject({
      method: 'GET',
      url: '/admin/cache/policies/history',
      params: expect.objectContaining({ key: query.get('key'), limit: 20, after_rev: Number(query.get('after_rev')) }),
    })
    // §7.6: with after_rev only rev_after > after_rev comes back, so a failed attempt (rev_after null) never does.
    expect(history.items.map((i) => i.rev_after)).toEqual([5])
    expect(history.items[0]).toMatchObject({ rev_before: 4, rev_after: 5, changed_fields: ['rollout_percent'] })
  })

  it('C-5 turns rev_stale into the three-way merge before any confirmation', async () => {
    respond(sample('list_m3_masked_by_master'))
    const state = setup()
    await state.load()
    state.update('upstream_identity', 'rollout_percent', 60)
    // Someone on call set the share back to 0 meanwhile (rev 5).
    const latest = rewritten(identityEntry(), 5, { rollout_percent: 0 }, 'oncall@example.com', '错误率上升，先调回 0')
    respond(sample('validate_rev_stale'), ok(latest), sample('history_after_rev'))
    await state.openPreview('upstream_identity')
    await flushPromises()
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      'GET /admin/cache/policies',
      'POST /admin/cache/policies/upstream_identity/validate',
      'GET /admin/cache/policies/upstream_identity',
      'GET /admin/cache/policies/history',
    ])
    expect(calls[3].params).toMatchObject({ key: 'upstream_identity', after_rev: 4 })
    const preview = state.preview.value!
    expect(preview.result).toBeNull()
    expect(preview.conflict).toMatchObject({ baseRev: 4, latestRev: 5, theirFields: ['rollout_percent'], merged: [] })
    expect(preview.conflict?.overlaps).toEqual([{ field: 'rollout_percent', base: 50, mine: 60, theirs: 0, choice: null, reverting: true }])
    // The writes in between, as the history answers for after_rev (§7.6).
    expect(preview.conflict?.writes?.map((w) => w.rev_after)).toEqual([5])

    state.setChoice('rollout_percent', 'theirs')
    await state.resolveConflict()
    expect(state.preview.value?.nothingLeft).toBe(true)
    expect(calls).toHaveLength(4)
  })

  it('C-6 parses the errors JSON string of a 400, degrades when it is unreadable, and names a 404', async () => {
    respond(sample('put_400'))
    expect(toWriteFailure(await rejected(putPolicy('observability', { expected_rev: 3, spec: { byte_budget_mib: 128 } })))).toEqual({
      kind: 'invalid',
      errors: [
        { field: 'byte_budget_mib', code: 'exceeds_static_cap', limit: { max: 64 } },
        { field: 'unknown_x', code: 'unknown_field' },
      ],
    })
    const unreadable = sample('put_400')
    unreadable.response.metadata.errors = '[{"field":'
    respond(unreadable)
    expect(toWriteFailure(await rejected(putPolicy('observability', { expected_rev: 3, spec: {} })))).toEqual({ kind: 'invalid', errors: [] })
    respond(sample('put_404'))
    expect(toWriteFailure(await rejected(putPolicy('routing', { expected_rev: 0, spec: {} })))).toEqual({ kind: 'not_in_build' })
  })

  it('C-7 sends only the changed top-level fields with expected_rev', async () => {
    respond(sample('list_default_all_off'))
    const state = setup()
    await state.load()
    state.update('observability', 'rate_mib_per_s', 16)
    respond(sample('validate_ok_disable'))
    await state.openPreview('observability')
    const expected = sample('put_partial_update').request.body as { spec: unknown }
    respond(sample('put_partial_update'), sample('list_default_all_off'))
    expect(await state.submit({ reason: ' 降低速率上限 ', checkbox: false, typed: '' })).toBe(true)
    expect(calls[2]).toMatchObject({ method: 'PUT', url: '/admin/cache/policies/observability' })
    expect(calls[2].data).toEqual({ expected_rev: 0, spec: expected.spec, reason: '降低速率上限', confirm: { checkbox: false, typed: null } })
  })

  it('C-8 re-reads the list after writing the master, so the gated key leaves masked_by_master', async () => {
    respond(sample('list_m3_masked_by_master'))
    const state = setup()
    await state.load()
    expect(state.stateOf('upstream_identity')).toBe('masked_by_master')
    respond(sample('validate_ok_master_enable_m3'))
    await state.openPreview('master', { enabled: true })
    respond(ok(entryOf('list_m3_master_on', 'master')), sample('list_m3_master_on'))
    expect(await state.submit({ reason: '开始试运行', checkbox: true, typed: '' })).toBe(true)
    await flushPromises()
    expect(calls.map((c) => `${c.method} ${c.url}`).slice(-2)).toEqual(['PUT /admin/cache/policies/master', 'GET /admin/cache/policies'])
    expect(state.stateOf('master')).toBe('on')
    expect(state.stateOf('upstream_identity')).toBe('on')
  })

  it('C-9 re-reads when a pending_reload countdown runs out, then once a refresh period while it stays pending, three times in all; shows a parse error as such', async () => {
    respond(sample('list_pending_reload'))
    const state = setup({ now: '2026-10-23T08:59:50Z' })
    await state.load()
    expect(state.stateOf('observability')).toBe('pending_reload')
    expect(state.deadlineOf('observability')?.at.toISOString()).toBe('2026-10-23T09:00:40.000Z')
    expect(state.hasCountdown()).toBe(true)
    state.tick(new Date('2026-10-23T09:00:20Z'))
    expect(calls).toHaveLength(1)
    // An instance that loads late keeps the key pending after the first re-read (n-new-2); refresh_interval_s is 60 here.
    respond(sample('list_pending_reload'), sample('list_pending_reload'), sample('list_pending_reload'))
    const reads: number[] = []
    for (const at of ['09:00:41', '09:00:42', '09:01:39', '09:01:41', '09:02:41', '09:03:41', '09:05:00']) {
      state.tick(new Date(`2026-10-23T${at}Z`))
      await flushPromises()
      reads.push(calls.length)
    }
    expect(reads).toEqual([2, 2, 2, 3, 4, 4, 4])
    expect(state.stateOf('observability')).toBe('pending_reload')
    expect(state.hasCountdown()).toBe(false)

    respond(sample('list_parse_error'))
    const broken = setup()
    await broken.load()
    expect(broken.stateOf('observability')).toBe('parse_error')
    expect(broken.policy('observability')?.effective.blockers).toEqual(['parse_error'])
  })

  it('C-10 clips a value above a lowered cap without blocking other edits', async () => {
    respond(sample('list_clipped_static_cap'))
    const state = setup()
    await state.load()
    const e = state.policy('observability')!
    expect(e.effective.clipped_fields).toEqual(['byte_budget_mib'])
    expect(e.schema.find((f) => f.field === 'byte_budget_mib')?.max).toBe(32)
    expect(state.issues('observability')).toEqual([])
    state.update('observability', 'chain_percent', 50)
    expect(state.issues('observability')).toEqual([])
    state.update('observability', 'byte_budget_mib', 48)
    expect(state.issues('observability')).toEqual([expect.objectContaining({ field: 'byte_budget_mib', code: 'exceeds_static_cap', params: expect.objectContaining({ limit: 32 }) })])
  })

  it('C-11 shows every key killed under the kill switch and still saves', async () => {
    respond(sample('list_kill_switch'))
    const state = setup()
    await state.load()
    expect([state.stateOf('master'), state.stateOf('observability')]).toEqual(['killed', 'killed'])
    expect(state.policy('observability')?.stored.spec.enabled).toBe(true)
    state.update('observability', 'enabled', false)
    expect(state.dirtyKeys.value).toEqual(['observability'])
    respond(sample('validate_ok_disable'))
    await state.openPreview('observability')
    expect(calls[1]).toMatchObject({ method: 'POST', data: { expected_rev: 3, spec: { enabled: false } } })
    expect(state.preview.value?.result?.direction).toBe('disable')
  })

  it('C-12 lists failed writes too, with field names only', async () => {
    respond(sample('history'))
    const history = await listHistory('upstream_identity')
    // The change log asks without after_rev, so failed attempts are listed too.
    expect(new URLSearchParams(sample('history').request.path.split('?')[1]).has('after_rev')).toBe(false)
    expect(calls[0].params).not.toHaveProperty('after_rev')
    expect(history.items.every((i) => /^admin\.cache\.policies\.[a-z_]+\.update$/.test(i.action))).toBe(true)
    expect(history.items[1]).toMatchObject({ status_code: 422, rev_before: null, rev_after: null, changed_fields: [] })
    expect(history.items.flatMap((i) => i.changed_fields).every((f) => /^[a-z_.]+$/.test(f))).toBe(true)
  })

  it('C-12 says why a change log is incomplete, for every history sample (v1.7.4.6)', async () => {
    const expected: Record<string, [boolean, string | null]> = {
      history: [true, null],
      history_after_rev: [true, null],
      history_pending_flush: [true, null],
      history_empty_within_window: [true, null],
      history_empty_outside_window: [false, 'earlier_missing'],
      history_truncated_retention: [false, 'earlier_missing'],
      history_gap: [false, 'gap'],
    }
    expect(SAMPLE_NAMES.filter((n) => n.startsWith('history')).sort()).toEqual(Object.keys(expected).sort())
    for (const [name, [complete, reason]] of Object.entries(expected)) {
      const s = sample(name)
      const query = new URLSearchParams(s.request.path.split('?')[1])
      const limit = Number(query.get('limit') ?? 20)
      const afterRev = query.get('after_rev')
      respond(s)
      const history = await listHistory(query.get('key')!, limit, afterRev === null ? undefined : Number(afterRev))
      const params = calls[calls.length - 1].params
      expect(params, name).toMatchObject({ key: query.get('key'), limit })
      expect('after_rev' in params, name).toBe(afterRev !== null)
      expect([history.history_complete, history.history_incomplete_reason], name).toEqual([complete, reason])
    }
  })

  it('C-13 treats the platform 403 as a plain failure and derives fallback groups from availability_fallback_group_id', async () => {
    respond(sample('put_403'))
    expect(toWriteFailure(await rejected(putPolicy('observability', { expected_rev: 0, spec: { enabled: true } })))).toEqual({
      kind: 'failed',
      message: 'Admin access required',
    })
    respond(sample('put_403'))
    const denied = setup()
    await denied.load()
    expect(denied.loadError.value).toBe('failed')

    respond(sample('list_default_all_off'))
    const state = setup({
      groups: [
        { id: 1, name: 'g-1', availability_fallback_group_id: 9 },
        { id: 9, name: 'g-9', availability_fallback_group_id: null },
      ],
    })
    await state.load()
    expect([...state.fallbackGroupIds.value]).toEqual([9])
    expect(SAMPLE_NAMES.map((n) => JSON.stringify(sample(n))).join('')).not.toContain('is_fallback')
  })
})

describe('contract samples', () => {
  it('cover C-1 to C-14 and keep the success and error envelopes apart', () => {
    const covered = new Set(SAMPLE_NAMES.flatMap((n) => sample(n)._contract))
    for (let i = 1; i <= 14; i++) expect(covered.has(`C-${i}`), `C-${i}`).toBe(true)
    for (const name of SAMPLE_NAMES) {
      const s = sample(name)
      if (s.status < 400) expect(s.response, name).toMatchObject({ code: 0, message: 'success' })
      else if (s.status !== 403) {
        expect(s.response.code, name).toBe(s.status)
        expect('data' in s.response, name).toBe(false)
        expect(Object.values(s.response.metadata).every((v) => typeof v === 'string'), name).toBe(true)
      }
    }
    // Keys outside the build never reach a draft, even through a type assertion.
    const outside = entryOf('list_default_all_off', 'routing') as PolicyEntry
    expect(isAvailable(outside)).toBe(false)
  })
})
