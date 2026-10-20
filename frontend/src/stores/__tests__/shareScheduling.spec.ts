import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useShareSchedulingStore } from '../shareScheduling'
import { shareGroup, sharePreview, shareReport, shareView } from './shareSchedulingFixtures'

const api = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  listGroups: vi.fn(),
  getPolicy: vi.fn(),
  updatePolicy: vi.fn(),
  preview: vi.fn(),
  report: vi.fn()
}))
vi.mock('@/api/admin/shareScheduling', () => ({ default: api }))

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  api.getSettings.mockResolvedValue({ enabled: true })
  api.updateSettings.mockImplementation(async (s: { enabled: boolean }) => s)
  api.listGroups.mockResolvedValue([shareGroup(5, { configured: true, version: 1 }), shareGroup(6, { scheduler: 'share', configured: true, version: 4, effective: true })])
  api.getPolicy.mockImplementation(async (id: number) => shareView(id))
  api.report.mockImplementation(async (id: number) => shareReport(id))
  api.preview.mockResolvedValue(sharePreview())
  api.updatePolicy.mockImplementation(async (id: number, update: { scheduler: 'sub2api' | 'share'; session_keep: boolean; expected_version: number }) =>
    shareView(id, { scheduler: update.scheduler, session_keep: update.session_keep, version: update.expected_version + 1, effective: update.scheduler === 'share' }))
})

async function loadedStore(groupId = 5) {
  const store = useShareSchedulingStore()
  await store.loadSettings()
  await store.loadGroups()
  await store.select(groupId)
  return store
}

describe('share scheduling store', () => {
  it('splits groups into those using share scheduling and the rest', async () => {
    const store = await loadedStore()
    expect(store.activeGroups.map(g => g.group_id)).toEqual([6])
    expect(store.idleGroups.map(g => g.group_id)).toEqual([5])
    expect(store.groupNames.get(6)).toBe('group-6')
  })

  it('loads the group view and its report together', async () => {
    const store = await loadedStore()
    expect(api.getPolicy).toHaveBeenCalledWith(5)
    expect(api.report).toHaveBeenCalledWith(5, '1h')
    expect(store.view?.group_id).toBe(5)
    expect(store.report?.group_id).toBe(5)
    await store.setReportWindow('24h')
    expect(api.report).toHaveBeenLastCalledWith(5, '24h')
  })

  it('previews a switch to share, then saves it with the loaded version', async () => {
    const store = await loadedStore()
    await store.requestChange({ scheduler: 'share' })
    expect(api.preview).toHaveBeenCalledWith(5, { scheduler: 'share', session_keep: true, expected_version: 1 })
    expect(store.pending?.preview?.members).toHaveLength(3)
    expect(store.pending?.enableGlobal).toBe(false)
    expect(api.updatePolicy).not.toHaveBeenCalled()

    expect(await store.confirmChange()).toBe(true)
    expect(api.updatePolicy).toHaveBeenCalledWith(5, { scheduler: 'share', session_keep: true, expected_version: 1 })
    expect(store.view).toMatchObject({ scheduler: 'share', version: 2 })
    expect(store.groups.find(g => g.group_id === 5)).toMatchObject({ scheduler: 'share', version: 2, effective: true })
    expect(store.pending).toBeNull()
  })

  it('goes back to the original scheduler without a preview', async () => {
    const store = await loadedStore(6)
    api.getPolicy.mockResolvedValueOnce(shareView(6, { scheduler: 'share', version: 4 }))
    await store.reload()
    await store.requestChange({ scheduler: 'sub2api' })
    expect(api.preview).not.toHaveBeenCalled()
    expect(await store.confirmChange()).toBe(true)
    expect(api.updatePolicy).toHaveBeenCalledWith(6, { scheduler: 'sub2api', session_keep: true, expected_version: 4 })
  })

  it('keeps the scheduler when only session keeping changes', async () => {
    const store = await loadedStore()
    await store.requestChange({ session_keep: false })
    await store.confirmChange()
    expect(api.updatePolicy).toHaveBeenCalledWith(5, { scheduler: 'sub2api', session_keep: false, expected_version: 1 })
  })

  it('saves a session keeping change of a share group without a preview or the main switch', async () => {
    api.getPolicy.mockImplementation(async (id: number) => shareView(id, { scheduler: 'share', effective: false, version: 4 }))
    const store = await loadedStore(6)
    store.setGlobalEnabled(false)
    await store.requestChange({ session_keep: false })
    // Nothing to wait for: the confirmation is ready at once.
    expect(store.previewing).toBe(false)
    expect(api.preview).not.toHaveBeenCalled()
    expect(store.pending?.enableGlobal).toBe(false)
    expect(await store.confirmChange()).toBe(true)
    expect(api.updateSettings).not.toHaveBeenCalled()
    expect(api.updatePolicy).toHaveBeenCalledWith(6, { scheduler: 'share', session_keep: false, expected_version: 4 })
  })

  it('turns the main switch on in the same confirmation when it is off', async () => {
    api.preview.mockResolvedValue(sharePreview({ enabled: false, effective_after: false }))
    const store = await loadedStore()
    store.setGlobalEnabled(false)
    await store.requestChange({ scheduler: 'share' })
    expect(store.pending?.enableGlobal).toBe(true)
    expect(await store.confirmChange()).toBe(true)
    expect(api.updateSettings).toHaveBeenCalledWith({ enabled: true })
    // The group is saved first: while the switch is off that changes nothing that runs.
    expect(api.updatePolicy.mock.invocationCallOrder[0]).toBeLessThan(api.updateSettings.mock.invocationCallOrder[0])
    expect(store.settings?.enabled).toBe(true)
    expect(store.view).toMatchObject({ scheduler: 'share', effective: true })
    expect(store.globalError).toBeNull()
  })

  it('leaves the main switch off when the group cannot be saved', async () => {
    api.preview.mockResolvedValue(sharePreview({ enabled: false, effective_after: false }))
    const store = await loadedStore()
    store.setGlobalEnabled(false)
    api.updatePolicy.mockRejectedValueOnce({ status: 409, reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })
    await store.requestChange({ scheduler: 'share' })
    expect(await store.confirmChange()).toBe(false)
    expect(api.updateSettings).not.toHaveBeenCalled()
    expect(store.settings?.enabled).toBe(false)
    expect(store.conflict).toBe(true)
  })

  it('says so when the group was saved but the main switch did not turn on', async () => {
    api.preview.mockResolvedValue(sharePreview({ enabled: false, effective_after: false }))
    api.updatePolicy.mockImplementation(async (id: number, update: { scheduler: 'sub2api' | 'share'; session_keep: boolean; expected_version: number }) =>
      shareView(id, { scheduler: update.scheduler, session_keep: update.session_keep, version: update.expected_version + 1, enabled: false, effective: false }))
    const store = await loadedStore()
    store.setGlobalEnabled(false)
    api.updateSettings.mockRejectedValueOnce({ status: 500, message: 'settings down' })
    await store.requestChange({ scheduler: 'share' })
    expect(await store.confirmChange()).toBe(true)
    expect(store.pending).toBeNull()
    expect(store.view).toMatchObject({ scheduler: 'share', version: 2, effective: false })
    expect(store.settings?.enabled).toBe(false)
    expect(store.globalError).toBe('settings down')
    // Turning the switch on from the page clears it.
    store.setGlobalEnabled(true)
    expect(store.globalError).toBeNull()
  })

  it('stops on a version conflict and asks for a reload', async () => {
    const store = await loadedStore()
    api.updatePolicy.mockRejectedValueOnce({ status: 409, reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })
    await store.requestChange({ scheduler: 'share' })
    expect(await store.confirmChange()).toBe(false)
    expect(store.conflict).toBe(true)
    expect(store.pending).toBeNull()
    api.getPolicy.mockResolvedValueOnce(shareView(5, { version: 7 }))
    await store.reload()
    expect(store.conflict).toBe(false)
    expect(store.view?.version).toBe(7)
  })

  it('keeps the dialog open with the error on other failures', async () => {
    const store = await loadedStore()
    api.updatePolicy.mockRejectedValueOnce({ status: 500, message: 'boom' })
    await store.requestChange({ scheduler: 'share' })
    expect(await store.confirmChange()).toBe(false)
    expect(store.pending).not.toBeNull()
    expect(store.actionError).toBe('boom')
    expect(store.conflict).toBe(false)
  })

  it('shows a preview failure but still allows confirming', async () => {
    api.preview.mockRejectedValueOnce({ message: 'preview down' })
    const store = await loadedStore()
    await store.requestChange({ scheduler: 'share' })
    expect(store.pending?.previewError).toBe('preview down')
    expect(store.previewing).toBe(false)
  })

  it('drops a pending change when another group is selected', async () => {
    const store = await loadedStore()
    await store.requestChange({ scheduler: 'share' })
    await store.select(6)
    expect(store.pending).toBeNull()
    expect(await store.confirmChange()).toBe(false)
    expect(api.updatePolicy).not.toHaveBeenCalled()
  })

  it('ignores a view that arrives after another group was selected', async () => {
    const store = useShareSchedulingStore()
    let resolveFirst: (value: unknown) => void = () => {}
    api.getPolicy.mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
    const first = store.select(5)
    await store.select(6)
    resolveFirst(shareView(5))
    await first
    expect(store.view?.group_id).toBe(6)
  })

  it('marks the storage unavailable on 503', async () => {
    api.getPolicy.mockRejectedValueOnce({ status: 503, reason: 'SHARE_SCHEDULING_UNAVAILABLE' })
    const store = useShareSchedulingStore()
    await store.select(5)
    expect(store.policyState).toBe('unavailable')
  })

  it('applies the main switch to every group summary', async () => {
    api.getPolicy.mockImplementation(async (id: number) => shareView(id, { scheduler: 'share', effective: true }))
    const store = await loadedStore(6)
    store.setGlobalEnabled(false)
    expect(store.groups.find(g => g.group_id === 6)?.effective).toBe(false)
    expect(store.view?.enabled).toBe(false)
    store.setGlobalEnabled(true)
    expect(store.groups.find(g => g.group_id === 6)?.effective).toBe(true)
  })
})
