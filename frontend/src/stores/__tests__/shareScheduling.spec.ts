import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useShareSchedulingStore } from '../shareScheduling'
import { shareMember, sharePolicy, shareView } from './shareSchedulingFixtures'

const api = vi.hoisted(() => ({
  getSettings: vi.fn(),
  listGroups: vi.fn(),
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  preview: vi.fn(),
  rebalance: vi.fn()
}))
vi.mock('@/api/admin/shareScheduling', () => ({ default: api }))

const preview = { active_tier: 1, members: [], placement_change: 0.1, sampled_keys: 200, estimated_migrations: 0, warnings: [] }

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  api.listGroups.mockResolvedValue([
    { group_id: 5, name: 'g5', platform: 'anthropic', scheduler: 'sub2api', version: 1, configured: true, effective: false, requests_1h: 0, cache_read_ratio_1h: 0, scheduling_miss_ratio_1h: 0 },
    { group_id: 6, name: 'g6', platform: 'anthropic', scheduler: 'sub2api', version: 0, configured: false, effective: false, requests_1h: 0, cache_read_ratio_1h: 0, scheduling_miss_ratio_1h: 0 }
  ])
  api.getPolicy.mockImplementation(async (id: number) => shareView(id))
  api.preview.mockResolvedValue(preview)
})

async function editedStore() {
  const store = useShareSchedulingStore()
  await store.loadGroups()
  await store.select(5)
  Object.assign(store.current!.draft.rows[0], { configured: true, weight: 30 })
  store.current!.draft.policy.scheduler = 'share'
  return store
}

describe('share scheduling store', () => {
  it('keeps each group draft while switching groups', async () => {
    const store = await editedStore()
    expect(store.dirty).toBe(true)
    await store.select(6)
    expect(store.dirty).toBe(false)
    expect([...store.draftGroups]).toEqual([5])
    await store.select(5)
    expect(api.getPolicy).toHaveBeenCalledTimes(2)
    expect(store.current!.draft.rows[0].weight).toBe(30)
  })

  it('validates locally before asking for a preview', async () => {
    const store = await editedStore()
    store.current!.draft.rows[0].weight = 0
    expect(await store.requestPreview()).toBe(false)
    expect(store.problems.map(p => p.key)).toEqual(['weightRange'])
    expect(api.preview).not.toHaveBeenCalled()
  })

  it('saves only the previewed draft with the expected version', async () => {
    const store = await editedStore()
    expect(await store.save()).toBe(false)
    expect(await store.requestPreview()).toBe(true)
    store.current!.draft.rows[1].configured = true
    expect(await store.save()).toBe(false)
    expect(api.updatePolicy).not.toHaveBeenCalled()

    expect(await store.requestPreview()).toBe(true)
    api.updatePolicy.mockImplementation(async (id: number, _version: number, policy) => shareView(id, { version: 2, scheduler: 'share', policy }))
    expect(await store.save()).toBe(true)
    expect(api.updatePolicy).toHaveBeenCalledWith(5, 1, expect.objectContaining({
      scheduler: 'share',
      accounts: [{ account_id: 1, tier: 1, weight: 30 }, { account_id: 2, tier: 1, weight: 10 }]
    }))
    expect(store.dirty).toBe(false)
    expect(store.savedVersion).toBe(2)
    expect(store.groups[0]).toMatchObject({ version: 2, scheduler: 'share' })
  })

  it('keeps the draft on a conflict and rebases it onto the latest version', async () => {
    const store = await editedStore()
    await store.requestPreview()
    api.updatePolicy.mockRejectedValue({ status: 409, reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })
    const latest = shareView(5, {
      version: 3,
      policy: sharePolicy({ accounts: [{ account_id: 1, tier: 5, weight: 5 }, { account_id: 2, tier: 2, weight: 4 }], cache: { ttl_seconds: 600, realign: 'off' } }),
      members: [shareMember(1), shareMember(2), shareMember(3)]
    })
    api.getPolicy.mockResolvedValue(latest)
    expect(await store.save()).toBe(false)
    expect(store.current!.conflict).toBe(3)
    expect(store.current!.draft.rows[0].weight).toBe(30)
    expect(store.dirty).toBe(true)

    await store.rebase()
    const entry = store.current!
    expect(entry.conflict).toBeNull()
    expect(entry.version).toBe(3)
    expect(store.rebasedVersion).toBe(3)
    expect(entry.draft.policy.scheduler).toBe('share')
    expect(entry.draft.policy.cache).toEqual({ ttl_seconds: 600, realign: 'off' })
    expect(entry.draft.rows.map(r => [r.account_id, r.configured, r.tier, r.weight])).toEqual([
      [1, true, 1, 30],
      [2, true, 2, 4],
      [3, false, 1, 10]
    ])
  })

  it('shows the server problems of a rejected preview', async () => {
    const store = await editedStore()
    api.preview.mockRejectedValue({ status: 400, reason: 'SHARE_SCHEDULING_POLICY_INVALID', metadata: { problems: 'account 9 is not a member of this group' } })
    expect(await store.requestPreview()).toBe(false)
    expect(store.serverProblems).toEqual(['account 9 is not a member of this group'])
    expect(store.actionError).toBe('')
  })

  it('marks storage outages separately from failures', async () => {
    const store = useShareSchedulingStore()
    api.listGroups.mockRejectedValue({ status: 503, reason: 'SHARE_SCHEDULING_UNAVAILABLE' })
    await store.loadGroups()
    expect(store.groupsState).toBe('unavailable')
    api.getPolicy.mockRejectedValue({ status: 500, message: 'boom' })
    await store.select(5)
    expect(store.policyState).toBe('failed')
    expect(store.policyError).toBe('boom')
  })

  it('ignores a slow policy response for a group no longer selected', async () => {
    const store = useShareSchedulingStore()
    let release: (value: unknown) => void = () => {}
    api.getPolicy.mockImplementationOnce(() => new Promise(resolve => { release = resolve }))
    const first = store.select(5)
    await store.select(6)
    release(shareView(5))
    await first
    expect(store.selected).toBe(6)
    expect(store.current?.view.group_id).toBe(6)
    expect(store.policyState).toBe('idle')
  })
})
