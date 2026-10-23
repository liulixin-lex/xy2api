import { beforeEach, describe, expect, it, vi } from 'vitest'

const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: http }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {} } }))

import { listHistory, policyApi, putPolicy, toWriteFailure, validatePolicy } from '../api'
import { usePolicies } from '../usePolicies'
import { identityEntry, m1Response, observabilityEntry, validation } from './fixtures'

function setup() {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(m1Response()),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validation()),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue({ items: [], history_complete: true }),
  }
  const state = usePolicies({ api: api as typeof policyApi, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-23T08:00:00Z') })
  return { api, state }
}

describe('cache policy api', () => {
  beforeEach(() => vi.clearAllMocks())

  it('calls the dedicated endpoints', async () => {
    http.post.mockResolvedValue({ data: validation() })
    http.put.mockResolvedValue({ data: observabilityEntry() })
    http.get.mockResolvedValue({ data: { items: [], history_complete: true } })
    await validatePolicy('observability', { expected_rev: 7, spec: { enabled: true } })
    await putPolicy('observability', { expected_rev: 7, spec: { enabled: true }, reason: 'r', confirm: { checkbox: false, typed: null } })
    await listHistory('observability')
    expect(http.post).toHaveBeenCalledWith('/admin/cache/policies/observability/validate', { expected_rev: 7, spec: { enabled: true } })
    expect(http.put.mock.calls[0][0]).toBe('/admin/cache/policies/observability')
    expect(http.get).toHaveBeenCalledWith('/admin/cache/policies/history', { params: { key: 'observability', limit: 20 } })
  })

  it('maps write errors to the cases the panel explains', () => {
    expect(toWriteFailure({ status: 409, code: 'CACHE_POLICY_REV_CONFLICT', metadata: { current_rev: 9 } })).toEqual({ kind: 'conflict', currentRev: 9 })
    expect(toWriteFailure({ status: 409, data: { current_rev: 4 } })).toEqual({ kind: 'conflict', currentRev: 4 })
    expect(toWriteFailure({ status: 409 })).toEqual({ kind: 'conflict', currentRev: null })
    expect(toWriteFailure({ status: 400, metadata: { errors: [{ field: 'x', code: 'out_of_range' }] } })).toEqual({ kind: 'invalid', errors: [{ field: 'x', code: 'out_of_range' }] })
    expect(toWriteFailure({ status: 422 })).toEqual({ kind: 'confirm' })
    expect(toWriteFailure({ status: 404 })).toEqual({ kind: 'not_in_build' })
    expect(toWriteFailure(new Error('boom'))).toEqual({ kind: 'failed', message: 'boom' })
  })
})

describe('usePolicies', () => {
  beforeEach(() => vi.clearAllMocks())

  it('reports a backend without the endpoints as unavailable', async () => {
    const { api, state } = setup()
    api.listPolicies.mockRejectedValueOnce({ status: 404 })
    await state.load()
    expect(state.loadError.value).toBe('unavailable')
  })

  it('validates a staged change against the loaded rev and writes it with expected_rev', async () => {
    const { api, state } = setup()
    await state.load()
    state.update('observability', 'enabled', true)
    expect(state.dirtyKeys.value).toEqual(['observability'])
    await state.openPreview('observability')
    expect(api.validatePolicy).toHaveBeenCalledWith('observability', { expected_rev: 7, spec: { enabled: true } })
    expect(api.putPolicy).not.toHaveBeenCalled()

    const saved = observabilityEntry({ stored: { ...observabilityEntry().stored, rev: 8, spec: { ...observabilityEntry().stored.spec, enabled: true } } })
    api.putPolicy.mockResolvedValueOnce(saved)
    expect(await state.submit({ reason: '  open  ', checkbox: false, typed: '' })).toBe(true)
    expect(api.putPolicy).toHaveBeenCalledWith('observability', {
      expected_rev: 7,
      spec: { enabled: true },
      reason: 'open',
      confirm: { checkbox: false, typed: null },
    })
    expect(state.preview.value).toBeNull()
    expect(state.dirtyKeys.value).toEqual([])
    expect(state.entry('observability')?.stored.rev).toBe(8)
    expect(state.receipts.value.observability.effectiveBy.toISOString()).toBe('2026-10-23T08:01:00.000Z')
  })

  it('turns a rev conflict into a friendly refresh that keeps the admin edits', async () => {
    const { api, state } = setup()
    api.listPolicies.mockResolvedValueOnce(m1Response({ identity: identityEntry() }))
    await state.load()
    state.update('upstream_identity', 'rollout_percent', 20)
    await state.openPreview('upstream_identity')
    api.putPolicy.mockRejectedValueOnce({ status: 409, code: 'CACHE_POLICY_REV_CONFLICT', metadata: { current_rev: 4 } })
    expect(await state.submit({ reason: 'widen', checkbox: true, typed: '' })).toBe(false)
    expect(state.preview.value?.failure).toEqual({ kind: 'conflict', currentRev: 4 })

    const fresh = identityEntry({ stored: { ...identityEntry().stored, rev: 4, spec: { ...identityEntry().stored.spec, forward_session_headers: 'normalize' } } })
    api.getPolicy.mockResolvedValueOnce(fresh)
    await state.refreshConflict()
    expect(api.getPolicy).toHaveBeenCalledWith('upstream_identity')
    expect(state.preview.value?.failure).toBeNull()
    expect(state.preview.value?.entry.stored.rev).toBe(4)
    expect(api.validatePolicy).toHaveBeenLastCalledWith('upstream_identity', { expected_rev: 4, spec: { rollout_percent: 20 } })
    expect(state.draftOf('upstream_identity')).toMatchObject({ rollout_percent: 20, forward_session_headers: 'normalize' })
  })

  it('previews the master switch directly and forgets it on cancel', async () => {
    const { api, state } = setup()
    await state.load()
    await state.openPreview('master', { enabled: true })
    expect(api.validatePolicy).toHaveBeenCalledWith('master', { expected_rev: 2, spec: { enabled: true } })
    state.closePreview()
    expect(state.preview.value).toBeNull()
    expect(state.draftOf('master')).toEqual({ enabled: false })
  })

  it('keeps everything read-only under the kill switch', async () => {
    const { api, state } = setup()
    api.listPolicies.mockResolvedValueOnce(m1Response({ kill: true }))
    await state.load()
    state.update('observability', 'enabled', true)
    expect(state.dirtyKeys.value).toEqual([])
    await state.openPreview('master', { enabled: true })
    expect(api.validatePolicy).not.toHaveBeenCalled()
  })
})
