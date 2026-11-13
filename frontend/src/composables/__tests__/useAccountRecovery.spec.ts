import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, watch } from 'vue'

import { isRecovering, recoveryLines, useAccountRecovery } from '../useAccountRecovery'
import { trackSchedReload, useSchedLayers } from '../useSchedLayers'

const api = vi.hoisted(() => ({ reset: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ manage: true })
})
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => perms.manage) }) }
})

function withRecovery() {
  let recovery!: ReturnType<typeof useAccountRecovery>
  const wrapper = mount(defineComponent({ setup() { recovery = useAccountRecovery(); return () => null } }))
  return { recovery, wrapper }
}

const clean = (id = 1) => ({ account_id: id, reset_at: '2026-11-12T12:00:00Z', cleared: ['quality', 'session_avoid'], kept: [], failed: [] })

beforeEach(() => {
  perms.manage = true
  api.reset.mockReset()
  recoveryLines.clear()
})

describe('recover (FINAL §5, contract §6.5)', () => {
  it('keeps the answer as the account\'s result line, a request error as a line without result, then refreshes', async () => {
    const { recovery } = withRecovery()
    const { changed } = useSchedLayers()
    const before = changed.value
    const refresh = vi.fn(async () => {})
    const kept = { ...clean(), kept: [{ kind: 'quota', until: '2026-11-14T00:00:00Z' }] }
    api.reset.mockResolvedValueOnce(kept)
    await recovery.recover(1, refresh)
    expect(api.reset).toHaveBeenCalledWith(1)
    expect(recoveryLines.get(1)?.result).toEqual(kept)
    expect(refresh).toHaveBeenCalledTimes(1)
    // The open drawer and board refresh too.
    expect(changed.value).toBe(before + 1)

    api.reset.mockRejectedValueOnce(new Error('down'))
    await recovery.recover(1)
    expect(recoveryLines.get(1)?.result).toBeNull()
  })

  it('holds one lock per account through the request and the refresh: a second click from any entry sends nothing', async () => {
    let answer!: (value: unknown) => void
    api.reset.mockReturnValueOnce(new Promise((resolve) => { answer = resolve }))
    let refreshed!: () => void
    const { recovery } = withRecovery()
    const other = withRecovery().recovery
    const first = recovery.recover(1, () => new Promise<void>((resolve) => { refreshed = resolve }))
    expect(isRecovering(1)).toBe(true)
    await other.recover(1)
    answer(clean())
    await flushPromises()
    expect(isRecovering(1)).toBe(true)
    await other.recover(1)
    expect(api.reset).toHaveBeenCalledTimes(1)
    refreshed()
    await first
    expect(isRecovering(1)).toBe(false)
  })

  it('holds the lock until the reload of an open layer is in, for a single and a batch recovery', async () => {
    api.reset.mockImplementation(async (id: number) => clean(id))
    const reloads: Array<() => void> = []
    const stop = watch(useSchedLayers().changed, () => trackSchedReload(new Promise<void>((resolve) => reloads.push(resolve))))
    try {
      const { recovery } = withRecovery()
      const single = recovery.recover(1, async () => {})
      await flushPromises()
      expect([reloads.length, isRecovering(1)]).toEqual([1, true])
      reloads[0]()
      await single
      expect(isRecovering(1)).toBe(false)

      const batch = recovery.recoverMany([2, 3], async () => {})
      await flushPromises()
      expect([isRecovering(2), isRecovering(3), recovery.batchRunning.value]).toEqual([true, true, true])
      reloads[1]()
      await batch
      expect([isRecovering(2), isRecovering(3), recovery.batchRunning.value]).toEqual([false, false, false])
    } finally {
      stop()
    }
  })

  it('sends nothing without accounts:manage', async () => {
    perms.manage = false
    const { recovery } = withRecovery()
    await recovery.recover(1)
    expect(await recovery.recoverMany([1, 2])).toEqual([])
    expect(api.reset).not.toHaveBeenCalled()
    expect(recoveryLines.size).toBe(0)
  })
})

describe('recoverMany (contract §6.5)', () => {
  it('runs four at a time, gives every account its own line and returns the ids that recovered fully', async () => {
    let running = 0
    let peak = 0
    api.reset.mockImplementation(async (id: number) => {
      running++
      peak = Math.max(peak, running)
      await new Promise((resolve) => setTimeout(resolve))
      running--
      if (id === 3) throw new Error('down')
      return id === 7 ? { ...clean(id), failed: [{ kind: 'rate_limit' }] } : clean(id)
    })
    const { recovery } = withRecovery()
    const refresh = vi.fn(async () => {})
    const recovered = await recovery.recoverMany(Array.from({ length: 12 }, (_, i) => i + 1), refresh)
    expect(peak).toBe(4)
    expect(recovered.sort((a, b) => a - b)).toEqual([1, 2, 4, 5, 6, 8, 9, 10, 11, 12])
    expect(recoveryLines.size).toBe(12)
    expect(recoveryLines.get(3)?.result).toBeNull()
    expect(recoveryLines.get(7)?.result?.failed).toEqual([{ kind: 'rate_limit' }])
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('locks its accounts against single recoveries, leaves out accounts already being recovered, and runs one batch at a time', async () => {
    const answers = new Map<number, (value: unknown) => void>()
    api.reset.mockImplementation((id: number) => new Promise((resolve) => answers.set(id, resolve)))
    const { recovery } = withRecovery()
    const single = recovery.recover(2)
    const batch = recovery.recoverMany([1, 2, 3])
    expect(recovery.batchRunning.value).toBe(true)
    expect([1, 2, 3].map(isRecovering)).toEqual([true, true, true])
    await recovery.recover(1)
    expect(await recovery.recoverMany([4])).toEqual([])
    expect(api.reset.mock.calls.map(([id]) => id)).toEqual([2, 1, 3])
    answers.forEach((resolve, id) => resolve(clean(id)))
    expect(await batch).toEqual([1, 3])
    await single
    expect([1, 2, 3].map(isRecovering)).toEqual([false, false, false])
    expect(recovery.batchRunning.value).toBe(false)
  })

  it('stops dispatching when accounts:manage is withdrawn or the page goes; answers already on their way still count', async () => {
    const answers = new Map<number, (value: unknown) => void>()
    api.reset.mockImplementation((id: number) => new Promise((resolve) => answers.set(id, resolve)))
    const { recovery } = withRecovery()
    const batch = recovery.recoverMany([1, 2, 3, 4, 5, 6])
    expect(api.reset).toHaveBeenCalledTimes(4)
    perms.manage = false
    answers.forEach((resolve, id) => resolve(clean(id)))
    expect(await batch).toEqual([1, 2, 3, 4])
    expect(api.reset).toHaveBeenCalledTimes(4)

    perms.manage = true
    answers.clear()
    api.reset.mockClear()
    const other = withRecovery()
    const next = other.recovery.recoverMany([11, 12, 13, 14, 15])
    other.wrapper.unmount()
    answers.forEach((resolve, id) => resolve(clean(id)))
    expect(await next).toEqual([11, 12, 13, 14])
    expect(api.reset).toHaveBeenCalledTimes(4)
  })
})
