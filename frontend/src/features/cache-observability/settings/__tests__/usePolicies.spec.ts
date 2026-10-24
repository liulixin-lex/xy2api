import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: http }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {} } }))

import { listHistory, policyApi, putPolicy, toWriteFailure, validatePolicy } from '../api'
import { usePolicies } from '../usePolicies'
import { history, rejection, validation as sampleValidation } from './contract'
import { identityEntry, m1Response, m3Response, observabilityEntry, rewritten, validation, withEntry } from './fixtures'

function setup(response = m1Response()) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(response),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validation()),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue(history()),
  }
  const later = vi.fn()
  const state = usePolicies({ api: api as typeof policyApi, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-23T09:00:00Z'), later })
  return { api, state, later }
}

describe('cache policy api', () => {
  beforeEach(() => vi.clearAllMocks())

  it('calls the dedicated endpoints', async () => {
    http.post.mockResolvedValue({ data: validation() })
    http.put.mockResolvedValue({ data: observabilityEntry() })
    http.get.mockResolvedValue({ data: history() })
    await validatePolicy('observability', { expected_rev: 0, spec: { enabled: true } })
    await putPolicy('observability', { expected_rev: 0, spec: { enabled: true }, reason: 'r', confirm: { checkbox: false, typed: null } })
    await listHistory('observability')
    await listHistory('upstream_identity', 20, 4)
    expect(http.post).toHaveBeenCalledWith('/admin/cache/policies/observability/validate', { expected_rev: 0, spec: { enabled: true } })
    expect(http.put.mock.calls[0][0]).toBe('/admin/cache/policies/observability')
    expect(http.get).toHaveBeenNthCalledWith(1, '/admin/cache/policies/history', { params: { key: 'observability', limit: 20 } })
    expect(http.get).toHaveBeenNthCalledWith(2, '/admin/cache/policies/history', { params: { key: 'upstream_identity', limit: 20, after_rev: 4 } })
  })

  it('matches errors by reason and parses the string metadata (M-1)', () => {
    expect(toWriteFailure(rejection('put_409'))).toEqual({ kind: 'conflict', currentRev: 5 })
    expect(toWriteFailure(rejection('put_400'))).toMatchObject({ kind: 'invalid', errors: [{ field: 'byte_budget_mib', code: 'exceeds_static_cap', limit: { max: 64 } }, { code: 'unknown_field' }] })
    expect(toWriteFailure(rejection('put_422'))).toEqual({ kind: 'confirm', missing: ['reason', 'checkbox', 'typed'], typeToConfirm: 'cache_routing_policy' })
    expect(toWriteFailure(rejection('put_404'))).toEqual({ kind: 'not_in_build' })
    expect(toWriteFailure(rejection('put_403'))).toEqual({ kind: 'failed', message: 'Admin access required' })
    // `code` is the numeric status; only `reason` names the error.
    expect(toWriteFailure({ ...rejection('put_409'), metadata: { current_rev: 'nine' } })).toEqual({ kind: 'conflict', currentRev: null })
    expect(toWriteFailure(new Error('boom'))).toEqual({ kind: 'failed', message: 'boom' })
  })
})

describe('usePolicies', () => {
  beforeEach(() => vi.clearAllMocks())

  it('reports a backend without the endpoints as unavailable', async () => {
    const { api, state } = setup()
    api.listPolicies.mockRejectedValueOnce(rejection('put_404'))
    await state.load()
    expect(state.loadError.value).toBe('unavailable')
  })

  it('validates against the loaded rev, writes with expected_rev, then re-reads every key and keeps other drafts (M-2)', async () => {
    const { api, state } = setup(m3Response())
    await state.load()
    state.update('upstream_identity', 'rollout_percent', 20)
    state.update('observability', 'sample_percent', 50)
    expect(state.dirtyKeys.value).toEqual(['observability', 'upstream_identity'])
    await state.openPreview('observability')
    const observability = state.policy('observability')!
    expect(api.validatePolicy).toHaveBeenCalledWith('observability', { expected_rev: observability.stored.rev, spec: { sample_percent: 50 } })
    expect(api.putPolicy).not.toHaveBeenCalled()

    const saved = rewritten(observability, observability.stored.rev + 1, { sample_percent: 50 })
    api.putPolicy.mockResolvedValueOnce(saved)
    api.listPolicies.mockResolvedValueOnce(withEntry(m3Response(), saved))
    expect(await state.submit({ reason: '  lower  ', checkbox: false, typed: '' })).toBe(true)
    expect(api.putPolicy).toHaveBeenCalledWith('observability', {
      expected_rev: observability.stored.rev,
      spec: { sample_percent: 50 },
      reason: 'lower',
      confirm: { checkbox: false, typed: null },
    })
    await flushPromises()
    expect(api.listPolicies).toHaveBeenCalledTimes(2)
    expect(state.preview.value).toBeNull()
    expect(state.dirtyKeys.value).toEqual(['upstream_identity'])
    expect(state.draftOf('upstream_identity').rollout_percent).toBe(20)
    expect(state.receipts.value.observability.effectiveBy.toISOString()).toBe('2026-10-23T09:01:00.000Z')
  })

  it('merges three ways after a 409: clean edits merge, a clash waits for a choice and is flagged when it reverts a rollback (M-3)', async () => {
    const { api, state } = setup(m3Response())
    await state.load()
    state.update('upstream_identity', 'rollout_percent', 60)
    state.update('upstream_identity', 'forward_session_headers', 'normalize')
    await state.openPreview('upstream_identity')
    api.putPolicy.mockRejectedValueOnce(rejection('put_409'))
    const latest = rewritten(identityEntry(), 5, { rollout_percent: 0, header_only_uuid: false })
    api.getPolicy.mockResolvedValueOnce(latest)
    expect(await state.submit({ reason: 'widen', checkbox: true, typed: '' })).toBe(false)
    await flushPromises()

    expect(api.getPolicy).toHaveBeenCalledWith('upstream_identity')
    expect(api.listHistory).toHaveBeenCalledWith('upstream_identity', 20, 4)
    const conflict = state.preview.value!.conflict!
    expect(conflict).toMatchObject({ baseRev: 4, latestRev: 5, theirFields: ['rollout_percent'], merged: ['forward_session_headers'] })
    expect(conflict.overlaps).toEqual([{ field: 'rollout_percent', base: 50, mine: 60, theirs: 0, choice: null, reverting: true }])
    // The mock answers with the full change log, failed attempt included (a backend ignoring after_rev): still only the write after the base.
    expect(conflict.writes?.map((w) => w.actor?.email)).toEqual(['oncall@example.com'])
    expect(state.preview.value?.result).toBeNull()

    state.setChoice('rollout_percent', 'mine')
    api.validatePolicy.mockResolvedValueOnce({ ...sampleValidation('validate_rev_stale'), current_rev: 5, rev_stale: false })
    await state.resolveConflict()
    expect(api.validatePolicy).toHaveBeenLastCalledWith('upstream_identity', { expected_rev: 5, spec: { rollout_percent: 60, forward_session_headers: 'normalize' } })
    expect(state.preview.value?.pendingDraft).toBeNull()
  })

  it('previews and merges list fields read back from the reactive drafts', async () => {
    const { api, state } = setup(m3Response())
    await state.load()
    state.update('upstream_identity', 'account_ids', [101, 103])
    await state.openPreview('upstream_identity')
    expect(api.validatePolicy).toHaveBeenLastCalledWith('upstream_identity', { expected_rev: 4, spec: { account_ids: [101, 103] } })

    api.putPolicy.mockRejectedValueOnce(rejection('put_409'))
    api.getPolicy.mockResolvedValueOnce(rewritten(identityEntry(), 5, { rollout_percent: 0 }))
    await state.submit({ reason: 'widen', checkbox: true, typed: '' })
    await flushPromises()
    expect(state.preview.value?.conflict).toMatchObject({ merged: ['account_ids'], overlaps: [] })
    expect(api.validatePolicy).toHaveBeenLastCalledWith('upstream_identity', { expected_rev: 5, spec: { account_ids: [101, 103] } })
  })

  it('stays closed when the admin cancels while the conflict reload is in flight', async () => {
    const { api, state } = setup(m3Response())
    await state.load()
    state.update('upstream_identity', 'rollout_percent', 60)
    await state.openPreview('upstream_identity')
    let answer: (value: unknown) => void = () => {}
    api.getPolicy.mockReturnValueOnce(new Promise((resolve) => (answer = resolve)))
    api.putPolicy.mockRejectedValueOnce(rejection('put_409'))
    await state.submit({ reason: 'widen', checkbox: true, typed: '' })
    expect(state.preview.value?.validating).toBe(true)
    expect(state.preview.value?.result).toBeNull()
    state.closePreview()
    answer(rewritten(identityEntry(), 5, { rollout_percent: 0 }))
    await flushPromises()
    expect(state.preview.value).toBeNull()
    // The fresh value still lands, so the card can say someone else wrote meanwhile.
    expect(state.staleBase('upstream_identity')?.stored.rev).toBe(5)
  })

  it('keeps the conflict and its refresh when the reload itself fails', async () => {
    const { api, state } = setup(m3Response())
    await state.load()
    state.update('upstream_identity', 'rollout_percent', 60)
    await state.openPreview('upstream_identity')
    api.putPolicy.mockRejectedValueOnce(rejection('put_409'))
    api.getPolicy.mockRejectedValueOnce(rejection('put_403'))
    await state.submit({ reason: 'widen', checkbox: true, typed: '' })
    await flushPromises()
    expect(state.preview.value).toMatchObject({ validating: false, result: null, validateFailure: { kind: 'conflict', currentRev: null } })
  })

  it('only saves under the kill switch: no "in effect by" countdown', async () => {
    const { api, state } = setup(m1Response({ kill: true }))
    await state.load()
    state.update('observability', 'enabled', true)
    await state.openPreview('observability')
    api.putPolicy.mockResolvedValueOnce(rewritten(observabilityEntry(), 1, { enabled: true }))
    expect(await state.submit({ reason: '', checkbox: false, typed: '' })).toBe(true)
    expect(state.receipts.value.observability).toMatchObject({ rev: 1, savedOnly: true, settled: true })
    expect(state.deadlineOf('observability')).toBeNull()
  })

  it('shows the confirmation controls a 422 says are still missing', async () => {
    const { api, state } = setup()
    await state.load()
    await state.openPreview('master', { enabled: true })
    api.putPolicy.mockRejectedValueOnce(rejection('put_422'))
    expect(await state.submit({ reason: '', checkbox: false, typed: '' })).toBe(false)
    expect(state.preview.value?.failure).toMatchObject({ kind: 'confirm' })
    expect(state.preview.value?.result?.confirm).toEqual({ reason_required: true, checkbox_required: true, type_to_confirm: 'cache_routing_policy' })
  })

  it('previews the master switch directly and forgets it on cancel', async () => {
    const { api, state } = setup()
    await state.load()
    await state.openPreview('master', { enabled: true })
    expect(api.validatePolicy).toHaveBeenCalledWith('master', { expected_rev: 0, spec: { enabled: true } })
    state.closePreview()
    expect(state.preview.value).toBeNull()
    expect(state.draftOf('master')).toEqual({ enabled: false })
  })

  it('keeps editing and saving under the kill switch, with every key shown as killed (FE-Q4)', async () => {
    const { api, state } = setup(m1Response({ kill: true }))
    await state.load()
    expect(state.stateOf('observability')).toBe('killed')
    state.update('observability', 'enabled', true)
    expect(state.dirtyKeys.value).toEqual(['observability'])
    await state.openPreview('observability')
    expect(api.validatePolicy).toHaveBeenCalledWith('observability', { expected_rev: 0, spec: { enabled: true } })
  })

  it('reads the change log once more, quietly, when it does not show a fresh write yet (§7.6)', async () => {
    const { api, state, later } = setup()
    await state.load()
    api.listHistory.mockResolvedValueOnce(history('history_pending_flush'))
    await state.loadHistory('observability')
    state.update('observability', 'enabled', true)
    await state.openPreview('observability')
    api.putPolicy.mockResolvedValueOnce(rewritten(observabilityEntry(), 3, { enabled: true }))
    // The audit writer has not flushed rev 3 yet: the log still ends at rev 2, and that is not an error.
    const flushing = history('history_pending_flush')
    api.listHistory.mockResolvedValueOnce(flushing)
    await state.submit({ reason: '', checkbox: false, typed: '' })
    await flushPromises()
    expect(state.histories.value.observability).toEqual(flushing)
    expect(later).toHaveBeenCalledTimes(1)
    expect(later.mock.calls[0][1]).toBe(2000)
    const saved = { ...flushing, items: [{ ...flushing.items[0], rev_before: 2, rev_after: 3 }, ...flushing.items] }
    api.listHistory.mockResolvedValueOnce(saved)
    later.mock.calls[0][0]()
    // The second read keeps the list on screen instead of a loading state.
    expect(state.histories.value.observability).toEqual(flushing)
    await flushPromises()
    expect(state.histories.value.observability).toEqual(saved)
    expect(later).toHaveBeenCalledTimes(1)
  })

  it('settles a receipt when its countdown ends and re-reads once', async () => {
    const { api, state } = setup()
    await state.load()
    state.update('observability', 'enabled', true)
    await state.openPreview('observability')
    api.putPolicy.mockResolvedValueOnce(rewritten(observabilityEntry(), 1, { enabled: true }))
    await state.submit({ reason: '', checkbox: false, typed: '' })
    await flushPromises()
    expect(api.listPolicies).toHaveBeenCalledTimes(2)
    expect(state.hasCountdown()).toBe(true)
    state.tick(new Date('2026-10-23T09:00:30Z'))
    state.tick(new Date('2026-10-23T09:01:01Z'))
    state.tick(new Date('2026-10-23T09:01:02Z'))
    await flushPromises()
    expect(api.listPolicies).toHaveBeenCalledTimes(3)
    expect(state.receipts.value.observability.settled).toBe(true)
    expect(state.hasCountdown()).toBe(false)
  })
})
