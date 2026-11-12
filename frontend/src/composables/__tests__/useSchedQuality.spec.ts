import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'

import type { SchedAccountsSummary } from '@/types/schedQuality'
import summaryFixture from '@/__tests__/fixtures/schedQuality/account_summary.json'

const { summary } = vi.hoisted(() => ({ summary: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: { summary } }))

import { secondsUntil, useSchedQualityClock, useSchedQualityPoll, useSchedQualitySummary } from '../useSchedQuality'

let hidden = false
const flush = () => new Promise((resolve) => setTimeout(resolve))

beforeEach(() => {
  hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
  summary.mockReset().mockResolvedValue(summaryFixture as SchedAccountsSummary)
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useSchedQualitySummary', () => {
  it('keys the summary by account and skips while the page is hidden or paused', async () => {
    const paused = ref(false)
    const scope = effectScope()
    const result = scope.run(() => useSchedQualitySummary(() => paused.value))!

    await result.refresh([479, 503], 'gpt-6.1-sol')
    expect(summary).toHaveBeenCalledWith([479, 503], 'gpt-6.1-sol', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(result.state.value).toBe('loaded')
    expect(result.byAccount.value.get(479)?.cooling[0].group_id).toBe(53)
    expect(result.view.value).toEqual({ at: '…', stale: false, global_enabled: true })

    hidden = true
    await result.refresh([479])
    hidden = false
    paused.value = true
    await result.refresh([479])
    expect(summary).toHaveBeenCalledTimes(1)

    paused.value = false
    await result.refresh([])
    expect(summary).toHaveBeenCalledTimes(1)
    expect(result.byAccount.value.size).toBe(0)
    scope.stop()
  })
})

describe('useSchedQualityPoll', () => {
  it('loads on open, again every 5 s while visible, and stops when closed', async () => {
    vi.useFakeTimers()
    const accountId = ref<number | null>(null)
    const load = vi.fn(async () => ({ id: accountId.value }))
    const scope = effectScope()
    const poll = scope.run(() => useSchedQualityPoll(accountId, load))!
    expect(load).not.toHaveBeenCalled()

    accountId.value = 479
    await nextTick()
    await vi.advanceTimersByTimeAsync(0)
    expect(load).toHaveBeenCalledTimes(1)
    expect(poll.data.value).toEqual({ id: 479 })

    await vi.advanceTimersByTimeAsync(5000)
    expect(load).toHaveBeenCalledTimes(2)

    hidden = true
    await vi.advanceTimersByTimeAsync(5000)
    expect(load).toHaveBeenCalledTimes(2)
    hidden = false
    await vi.advanceTimersByTimeAsync(5000)
    expect(load).toHaveBeenCalledTimes(3)

    accountId.value = null
    await nextTick()
    expect(poll.data.value).toBeNull()
    await vi.advanceTimersByTimeAsync(15_000)
    expect(load).toHaveBeenCalledTimes(3)
    scope.stop()
  })

  it('drops the response of the previous key', async () => {
    const key = ref<number | null>(1)
    let finishFirst: (value: string) => void = () => {}
    const load = vi.fn((signal: AbortSignal) =>
      key.value === 1
        ? new Promise<string>((resolve) => { finishFirst = resolve; signal.addEventListener('abort', () => resolve('aborted')) })
        : Promise.resolve('second')
    )
    const scope = effectScope()
    const poll = scope.run(() => useSchedQualityPoll(key, load))!

    key.value = 2
    await nextTick()
    await flush()
    finishFirst('first')
    await flush()
    expect(load.mock.calls[0][0].aborted).toBe(true)
    expect(poll.data.value).toBe('second')
    scope.stop()
  })
})

describe('cooling countdown', () => {
  it('counts down from until on the local clock without any request', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-11T12:04:00Z'))
    const scope = effectScope()
    const now = scope.run(() => useSchedQualityClock())!
    const until = '2026-11-11T12:04:30Z'
    expect(secondsUntil(until, now.value)).toBe(30)

    await vi.advanceTimersByTimeAsync(1000)
    expect(secondsUntil(until, now.value)).toBe(29)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(secondsUntil(until, now.value)).toBe(0)
    expect(secondsUntil(null, now.value)).toBeNull()
    expect(summary).not.toHaveBeenCalled()
    scope.stop()
  })
})
