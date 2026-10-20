import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import { useAccountShareColumn } from '../useAccountShareColumn'
import { shareMember, shareView } from '@/stores/__tests__/shareSchedulingFixtures'

const getPolicy = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/shareScheduling', () => ({ default: { getPolicy } }))

describe('useAccountShareColumn', () => {
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
})
