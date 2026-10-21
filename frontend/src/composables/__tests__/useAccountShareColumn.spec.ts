import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import { useAccountShareColumn } from '../useAccountShareColumn'
import { shareMember, shareView } from '@/stores/__tests__/shareSchedulingFixtures'

const getPolicy = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/shareScheduling', () => ({ default: { getPolicy } }))

describe('useAccountShareColumn', () => {
  beforeEach(() => {
    getPolicy.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows expected shares only while filtered to a share group', async () => {
    getPolicy.mockImplementation(async (id: number) => (id === 6
      ? shareView(6, { scheduler: 'share', members: [shareMember(1, { expected_share: 0.7 }), shareMember(2, { healthy: false, expected_share: 0, share_if_healthy: 0.3 })] })
      : shareView(id)))
    const group = ref('')
    const { shares } = useAccountShareColumn(() => group.value)
    await flushPromises()
    expect(shares.value).toBeNull()
    expect(getPolicy).not.toHaveBeenCalled()

    group.value = '6'
    await nextTick(); await flushPromises()
    expect(shares.value?.get(1)).toEqual({ expected_share: 0.7, share_if_healthy: 0.5, healthy: true })
    expect(shares.value?.get(2)?.share_if_healthy).toBe(0.3)

    group.value = '5'
    await nextTick(); await flushPromises()
    expect(shares.value).toBeNull()

    group.value = 'ungrouped'
    await nextTick(); await flushPromises()
    expect(shares.value).toBeNull()
    expect(getPolicy).toHaveBeenCalledTimes(2)
  })

  it('stays hidden when the view cannot be read', async () => {
    getPolicy.mockRejectedValueOnce(new Error('down'))
    const { shares } = useAccountShareColumn(() => '6')
    await flushPromises()
    expect(shares.value).toBeNull()
  })

  it('reads a group when the filter picks it, then at most once a minute', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(Date.parse('2026-10-20T12:00:00Z'))
    getPolicy.mockImplementation(async (id: number) => shareView(id, { scheduler: 'share', members: [shareMember(1, { expected_share: 0.7 })] }))
    const group = ref('6')
    const { shares, refresh } = useAccountShareColumn(() => group.value)
    // The list reloads right after the filter changes; that must not read again.
    void refresh()
    await flushPromises()
    expect(getPolicy).toHaveBeenCalledTimes(1)
    expect(shares.value?.get(1)?.expected_share).toBe(0.7)

    vi.setSystemTime(Date.parse('2026-10-20T12:00:59Z'))
    await refresh()
    expect(getPolicy).toHaveBeenCalledTimes(1)

    vi.setSystemTime(Date.parse('2026-10-20T12:01:00Z'))
    getPolicy.mockImplementation(async (id: number) => shareView(id, { scheduler: 'share', members: [shareMember(1, { expected_share: 0.4 })] }))
    await refresh()
    expect(getPolicy).toHaveBeenCalledTimes(2)
    expect(shares.value?.get(1)?.expected_share).toBe(0.4)
  })

  it('reuses a fresh read when the filter comes back, including a group that is not on share', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(Date.parse('2026-10-20T12:00:00Z'))
    getPolicy.mockImplementation(async (id: number) => (id === 6
      ? shareView(6, { scheduler: 'share', members: [shareMember(1, { expected_share: 0.7 })] })
      : shareView(id)))
    const group = ref('6')
    const { shares, refresh } = useAccountShareColumn(() => group.value)
    await flushPromises()
    group.value = '5'
    await nextTick(); await flushPromises()
    expect(shares.value).toBeNull()
    await refresh()
    group.value = '6'
    await nextTick(); await flushPromises()
    expect(shares.value?.get(1)?.expected_share).toBe(0.7)
    expect(getPolicy.mock.calls.map(call => call[0])).toEqual([6, 5])

    vi.setSystemTime(Date.parse('2026-10-20T12:02:00Z'))
    group.value = '5'
    await nextTick(); await flushPromises()
    expect(getPolicy.mock.calls.map(call => call[0])).toEqual([6, 5, 5])
  })

  it('does not retry a failed read on every reload', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(Date.parse('2026-10-20T12:00:00Z'))
    getPolicy.mockRejectedValue(new Error('down'))
    const { shares, refresh } = useAccountShareColumn(() => '6')
    await flushPromises()
    await refresh()
    expect(shares.value).toBeNull()
    expect(getPolicy).toHaveBeenCalledTimes(1)
    vi.setSystemTime(Date.parse('2026-10-20T12:01:00Z'))
    getPolicy.mockResolvedValue(shareView(6, { scheduler: 'share', members: [shareMember(1, { expected_share: 0.7 })] }))
    await refresh()
    expect(shares.value?.get(1)?.expected_share).toBe(0.7)
  })
})
