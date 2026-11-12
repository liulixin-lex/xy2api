import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

import { canRecover, isRecovering, useAccountRecovery, type RecoverableRow } from '../useAccountRecovery'
import { useSchedLayers } from '../useSchedLayers'

const api = vi.hoisted(() => ({ reset: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ manage: true })
})
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => perms.manage) }) }
})

const NOW = Date.parse('2026-11-12T12:00:00Z')
const at = (ms: number) => new Date(NOW + ms).toISOString()
const row = (patch: Partial<RecoverableRow> = {}): RecoverableRow =>
  ({ status: 'active', rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null, extra: {}, capability_learned: null, ...patch })
const summary = (cooling: number) => ({ groups: { total: 2, normal: 2 - cooling, cooling, nodata: 0 } })

describe('canRecover (contract delta F1, scenario 37)', () => {
  it('is off for a normal account and for states a recovery cannot clear', () => {
    expect(canRecover(row(), summary(0), NOW)).toBe(false)
    // Manual off, schedulable off, quota used up, IQ gate only: the row alone never offers it.
    expect(canRecover(row({ status: 'inactive' }), summary(0), NOW)).toBe(false)
    expect(canRecover({ ...row(), schedulable: false, quota_used: 10, quota_limit: 10 } as RecoverableRow, summary(0), NOW)).toBe(false)
    expect(canRecover(row({ extra: { model_rate_limits: { 'iq:gpt-6-astra': { rate_limited_at: at(-1000), rate_limit_reset_at: at(3_600_000) } } } }), summary(0), NOW)).toBe(false)
    // Overrides alone (no cooling group).
    expect(canRecover(row(), { ...summary(0), overrides: [{ group_id: 0, mode: 'exempt', expires_at: null }] } as never, NOW)).toBe(false)
  })

  it('is on for an error status, an unexpired account limit, a non-IQ model limit, a stream-only mark or a cooling group', () => {
    expect(canRecover(row({ status: 'error' }), undefined, NOW)).toBe(true)
    for (const field of ['rate_limit_reset_at', 'overload_until', 'temp_unschedulable_until'] as const) {
      expect(canRecover(row({ [field]: at(1000) }), undefined, NOW)).toBe(true)
    }
    expect(canRecover(row({ extra: { model_rate_limits: {
      'iq:gpt-6-astra': { rate_limited_at: at(-1000), rate_limit_reset_at: at(3_600_000) },
      'gpt-6.1-sol': { rate_limited_at: at(-1000), rate_limit_reset_at: at(60_000) }
    } } }), undefined, NOW)).toBe(true)
    expect(canRecover(row({ capability_learned: [{ protocol: 'responses', mode: 'stream_only', expires_at: at(60_000) }] }), undefined, NOW)).toBe(true)
    // A cooling group counts even when an override hides it from the groups column.
    expect(canRecover(row(), { ...summary(1), overrides: [{ group_id: 0, mode: 'exempt', expires_at: null }] } as never, NOW)).toBe(true)
  })

  it('counts an end time equal to now as ended, and a missing or invalid one as none', () => {
    for (const value of [at(0), at(-1), null, '', 'not-a-time']) {
      expect(canRecover(row({ rate_limit_reset_at: value, overload_until: value, temp_unschedulable_until: value }), undefined, NOW)).toBe(false)
      expect(canRecover(row({ extra: { model_rate_limits: { 'gpt-6.1-sol': { rate_limited_at: at(-1000), rate_limit_reset_at: value as string } } } }), undefined, NOW)).toBe(false)
      expect(canRecover(row({ capability_learned: [{ protocol: 'responses', mode: 'stream_only', expires_at: value as string }] }), undefined, NOW)).toBe(false)
    }
  })

  it('decides from the row alone until the summary is in', () => {
    expect(canRecover(row(), undefined, NOW)).toBe(false)
    expect(canRecover(row(), summary(1), NOW)).toBe(true)
  })
})

function withRecovery() {
  let api!: ReturnType<typeof useAccountRecovery>
  const wrapper = mount(defineComponent({ setup() { api = useAccountRecovery(); return () => null } }))
  return { recovery: api, wrapper }
}

beforeEach(() => {
  perms.manage = true
  api.reset.mockReset()
  toast.showSuccess.mockReset()
  toast.showError.mockReset()
})
afterEach(() => { vi.useRealTimers() })

const clean = { reset_at: 'x', cleared: [{ key: 'quality', kind: 'quality' }], preserved: [], failed: [] }

describe('recover (contract delta F2, scenarios 38–39)', () => {
  it.each([
    ['everything cleared', async () => clean, 'showSuccess', 'admin.schedQuality.recover.ok:{"name":"acc-1"}'],
    ['only kept items left', async () => ({ ...clean, preserved: [{ key: 'session_avoid', kind: 'session_avoid', why: '' }] }), 'showSuccess', 'admin.schedQuality.recover.ok:{"name":"acc-1"}'],
    ['a failed item in a 200 answer', async () => ({ ...clean, failed: [{ key: 'rate_limit', kind: 'rate_limit', error: 'x' }] }), 'showError', 'admin.schedQuality.recover.failed:{"name":"acc-1"}'],
    ['a request error', async () => { throw new Error('down') }, 'showError', 'admin.schedQuality.recover.failed:{"name":"acc-1"}']
  ] as const)('gives exactly one toast for %s, then refreshes', async (_name, answer, kind, text) => {
    api.reset.mockImplementation(answer)
    const { recovery } = withRecovery()
    const { changed } = useSchedLayers()
    const before = changed.value
    const refresh = vi.fn(async () => {})
    await recovery.recover({ id: 1, name: 'acc-1' }, refresh)
    expect(api.reset).toHaveBeenCalledWith(1)
    expect(toast[kind].mock.calls).toEqual([[text]])
    expect(toast[kind === 'showSuccess' ? 'showError' : 'showSuccess']).not.toHaveBeenCalled()
    expect(refresh).toHaveBeenCalledTimes(1)
    // The open drawer and board refresh too.
    expect(changed.value).toBe(before + 1)
  })

  it('holds one lock per account through the request and the refresh: a second click from any entry sends nothing', async () => {
    let answer!: (value: unknown) => void
    api.reset.mockReturnValueOnce(new Promise((resolve) => { answer = resolve }))
    let refreshed!: () => void
    const { recovery } = withRecovery()
    // Another entry (the drawer, the temporary-pause dialog) on another component shares the lock.
    const other = withRecovery().recovery
    const first = recovery.recover({ id: 1, name: 'acc-1' }, () => new Promise<void>((resolve) => { refreshed = resolve }))
    expect(isRecovering(1)).toBe(true)
    await other.recover({ id: 1, name: 'acc-1' })
    answer(clean)
    await flushPromises()
    expect(isRecovering(1)).toBe(true)
    await other.recover({ id: 1, name: 'acc-1' })
    expect(api.reset).toHaveBeenCalledTimes(1)
    refreshed()
    await first
    expect(isRecovering(1)).toBe(false)
  })

  it('checks accounts:manage at the click and sends nothing without it (scenario 41)', async () => {
    perms.manage = false
    const { recovery } = withRecovery()
    await recovery.recover({ id: 1, name: 'acc-1' })
    const result = await recovery.recoverMany([1, 2])
    expect(api.reset).not.toHaveBeenCalled()
    expect(result).toEqual({ recovered: [], failed: [] })
    expect(toast.showSuccess).not.toHaveBeenCalled()
    expect(toast.showError).not.toHaveBeenCalled()
  })
})

describe('recoverMany (contract delta F3, scenarios 40–41)', () => {
  it('runs four at a time, gives one toast with the counts and reports recovered and failed ids', async () => {
    let running = 0
    let peak = 0
    api.reset.mockImplementation(async (id: number) => {
      running++
      peak = Math.max(peak, running)
      await new Promise((resolve) => setTimeout(resolve))
      running--
      if (id === 3) throw new Error('down')
      return id === 7 ? { ...clean, failed: [{ key: 'rate_limit', kind: 'rate_limit', error: 'x' }] } : clean
    })
    const { recovery } = withRecovery()
    const refresh = vi.fn(async () => {})
    const result = await recovery.recoverMany(Array.from({ length: 12 }, (_, i) => i + 1), refresh)
    expect(peak).toBe(4)
    expect(result.failed.sort((a, b) => a - b)).toEqual([3, 7])
    expect(result.recovered).toHaveLength(10)
    expect(toast.showError.mock.calls).toEqual([['admin.schedQuality.recover.batchPartial:{"ok":10,"failed":2}']])
    expect(toast.showSuccess).not.toHaveBeenCalled()
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('says 已恢复 n when nothing failed', async () => {
    api.reset.mockResolvedValue(clean)
    const { recovery } = withRecovery()
    await recovery.recoverMany([1, 2, 3])
    expect(toast.showSuccess.mock.calls).toEqual([['admin.schedQuality.recover.batchOk:{"n":3}']])
  })

  it('locks its accounts against single recoveries, leaves out accounts already being recovered, and runs one batch at a time', async () => {
    const answers = new Map<number, (value: unknown) => void>()
    api.reset.mockImplementation((id: number) => new Promise((resolve) => answers.set(id, resolve)))
    const { recovery } = withRecovery()
    const single = recovery.recover({ id: 2, name: 'acc-2' })
    const batch = recovery.recoverMany([1, 2, 3])
    expect(recovery.batchRunning.value).toBe(true)
    expect([1, 2, 3].map(isRecovering)).toEqual([true, true, true])
    await recovery.recover({ id: 1, name: 'acc-1' })
    expect(await recovery.recoverMany([4])).toEqual({ recovered: [], failed: [] })
    expect(api.reset.mock.calls.map(([id]) => id)).toEqual([2, 1, 3])
    answers.forEach((resolve) => resolve(clean))
    expect(await batch).toEqual({ recovered: [1, 3], failed: [] })
    await single
    expect([1, 2, 3].map(isRecovering)).toEqual([false, false, false])
    expect(recovery.batchRunning.value).toBe(false)
  })

  it('stops dispatching when accounts:manage is withdrawn mid-run; answers already on their way still count', async () => {
    const answers = new Map<number, (value: unknown) => void>()
    api.reset.mockImplementation((id: number) => new Promise((resolve) => answers.set(id, resolve)))
    const { recovery } = withRecovery()
    const batch = recovery.recoverMany([1, 2, 3, 4, 5, 6])
    expect(api.reset).toHaveBeenCalledTimes(4)
    perms.manage = false
    answers.forEach((resolve) => resolve(clean))
    const result = await batch
    expect(api.reset).toHaveBeenCalledTimes(4)
    expect(result).toEqual({ recovered: [1, 2, 3, 4], failed: [] })
    expect(toast.showSuccess.mock.calls).toEqual([['admin.schedQuality.recover.batchOk:{"n":4}']])
  })

  it('stops dispatching when the page goes away', async () => {
    const answers = new Map<number, (value: unknown) => void>()
    api.reset.mockImplementation((id: number) => new Promise((resolve) => answers.set(id, resolve)))
    const { recovery, wrapper } = withRecovery()
    const batch = recovery.recoverMany([1, 2, 3, 4, 5, 6])
    wrapper.unmount()
    answers.forEach((resolve) => resolve(clean))
    expect((await batch).recovered).toEqual([1, 2, 3, 4])
    expect(api.reset).toHaveBeenCalledTimes(4)
  })
})
