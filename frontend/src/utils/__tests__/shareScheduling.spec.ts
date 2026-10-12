import { describe, expect, it } from 'vitest'
import {
  draftFingerprint,
  draftFromView,
  draftShares,
  fromLocalInput,
  isShareConflict,
  policyFromDraft,
  refreshShareDefaults,
  resetShareRow,
  sharePolicyProblems,
  toLocalInput,
  validateShareDraft
} from '../shareScheduling'
import { shareMember, sharePolicy, shareView } from '@/stores/__tests__/shareSchedulingFixtures'

describe('share scheduling drafts', () => {
  it('keeps explicit rules, defaults the rest and collects stale rules', () => {
    const view = shareView(5, {
      policy: sharePolicy({ accounts: [{ account_id: 1, tier: 3, weight: 7, cache_domain: 'ws-a' }, { account_id: 99, tier: 1, weight: 1 }] }),
      members: [shareMember(1), shareMember(2, { priority: 4, concurrency: 25 })]
    })
    const draft = draftFromView(view)
    expect(draft.rows.map(r => [r.account_id, r.configured, r.tier, r.weight, r.cache_domain])).toEqual([
      [1, true, 3, 7, 'ws-a'],
      [2, false, 4, 25, '']
    ])
    expect(draft.stale.map(r => r.account_id)).toEqual([99])
    expect(policyFromDraft(draft).accounts).toEqual([{ account_id: 1, tier: 3, weight: 7, cache_domain: 'ws-a' }])
    expect(view.policy.accounts).toHaveLength(2)
  })

  it('saves only configured rows and drops a deadline without drain', () => {
    const draft = draftFromView(shareView(5, { members: [shareMember(3), shareMember(1), shareMember(2)] }))
    Object.assign(draft.rows[0], { configured: true, weight: 4, drain: true, drain_until: '2026-10-11T00:00:00.000Z', cache_domain: '  ' })
    Object.assign(draft.rows[1], { configured: true, drain: false, drain_until: '2026-10-11T00:00:00.000Z' })
    expect(policyFromDraft(draft).accounts).toEqual([
      { account_id: 1, tier: 1, weight: 10 },
      { account_id: 3, tier: 1, weight: 4, drain: true, drain_until: '2026-10-11T00:00:00.000Z' }
    ])
  })

  it('resets rows to the defaults and follows the default weight mode', () => {
    const draft = draftFromView(shareView(5, { members: [shareMember(1, { concurrency: 0 }), shareMember(2, { concurrency: 3 })] }))
    expect(draft.rows.map(r => r.weight)).toEqual([1, 3])
    const before = draftFingerprint(draft)
    Object.assign(draft.rows[1], { configured: true, weight: 9, drain: true, cache_domain: 'x' })
    expect(draftFingerprint(draft)).not.toBe(before)
    resetShareRow(draft, draft.rows[1])
    expect(draftFingerprint(draft)).toBe(before)

    draft.policy.defaults.weight = 'equal'
    refreshShareDefaults(draft)
    expect(draft.rows.map(r => r.weight)).toEqual([1, 1])
  })

  it('reports the same problems as the backend', () => {
    const draft = draftFromView(shareView(5, { members: [shareMember(1), shareMember(2, { platform: 'openai' })] }))
    Object.assign(draft.rows[0], { configured: true, weight: 0, cache_domain: 'shared' })
    Object.assign(draft.rows[1], { configured: true, cache_domain: 'shared' })
    draft.policy.balance.correction = 'gentle'
    draft.policy.cache.ttl_seconds = 30
    expect(validateShareDraft(draft).map(p => p.key)).toEqual(['weightRange', 'cacheDomainPlatform', 'cacheTTL', 'correctionSessions'])

    const drained = draftFromView(shareView(5))
    for (const row of drained.rows) Object.assign(row, { configured: true, drain: true })
    expect(validateShareDraft(drained)).toEqual([])
    drained.policy.scheduler = 'share'
    expect(validateShareDraft(drained).map(p => p.key)).toEqual(['allDrained'])

    const bad = draftFromView(shareView(5))
    Object.assign(bad.rows[0], { configured: true, cache_domain: 'has space', tier: 1.5 })
    expect(validateShareDraft(bad).map(p => p.key)).toEqual(['tierRange', 'cacheDomain'])
    Object.assign(bad.rows[0], { cache_domain: 'account:2', tier: 1 })
    expect(validateShareDraft(bad).map(p => p.key)).toEqual(['cacheDomainReserved'])
  })

  it('computes shares like the backend, including negative tiers', () => {
    const draft = draftFromView(shareView(5, { members: [shareMember(1), shareMember(2), shareMember(3), shareMember(4)] }))
    const set = (i: number, tier: number, weight: number, drain = false) => Object.assign(draft.rows[i], { configured: true, tier, weight, drain })
    set(0, -5, 3); set(1, -5, 1); set(2, -9, 1); set(3, -5, 5, true)
    const healthy = new Set([1, 2])
    const shares = draftShares(draft, id => healthy.has(id))
    expect(shares.activeTier).toBe(-5)
    expect(shares.byAccount.get(1)).toEqual({ target: 0.75, effective: 0.75, capacity: 0.5 })
    expect(shares.byAccount.get(3)).toEqual({ target: 1, effective: 0, capacity: 1 })
    expect(shares.byAccount.get(4)).toEqual({ target: 0, effective: 0, capacity: 0 })
    expect(draftShares(draft, () => false).activeTier).toBeNull()
  })

  it('reads conflicts and server problems from API errors', () => {
    expect(isShareConflict({ status: 409 })).toBe(true)
    expect(isShareConflict({ reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })).toBe(true)
    expect(isShareConflict(new Error('x'))).toBe(false)
    expect(sharePolicyProblems({ reason: 'SHARE_SCHEDULING_POLICY_INVALID', metadata: { problems: 'a\nb' } })).toEqual(['a', 'b'])
    expect(sharePolicyProblems({ reason: 'OTHER', metadata: { problems: 'a' } })).toEqual([])
  })

  it('round-trips datetime-local values', () => {
    const iso = '2026-10-11T08:30:00.000Z'
    expect(fromLocalInput(toLocalInput(iso))).toBe(iso)
    expect(toLocalInput('')).toBe('')
    expect(fromLocalInput('nonsense')).toBe('')
  })
})

it('keeps stop, strict drain and emergency fallback distinct', () => {
  const draft = draftFromView(shareView(5, { policy: sharePolicy({ scheduler: 'share', accounts: [
    { account_id: 1, tier: 1, weight: 3, disabled: true },
    { account_id: 2, tier: 1, weight: 1, drain: true, drain_fallback: true }
  ] }) }))
  expect(validateShareDraft(draft)).toEqual([])
  expect(policyFromDraft(draft).accounts).toEqual([
    { account_id: 1, tier: 1, weight: 3, disabled: true },
    { account_id: 2, tier: 1, weight: 1, drain: true, drain_fallback: true }
  ])
  const shares = draftShares(draft, () => true)
  expect(shares.byAccount.get(1)?.effective).toBe(0)
  expect(shares.byAccount.get(2)?.effective).toBe(1)
  draft.rows[1].drain_fallback = false
  expect(validateShareDraft(draft).map(p => p.key)).toContain('allDrained')
  draft.rows[1].disabled = true
  expect(validateShareDraft(draft)).toEqual([])
  resetShareRow(draft, draft.rows[0])
  expect(draft.rows[0]).toMatchObject({ disabled: false, drain: false, drain_fallback: false })
})
