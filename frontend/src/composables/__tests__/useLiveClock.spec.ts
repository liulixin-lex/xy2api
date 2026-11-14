import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { noteServerDate, serverNow, useLiveClock } from '../useLiveClock'

let hidden = false
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-11-14T08:00:00.000Z'))
  hidden = false
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden })
})
afterEach(() => {
  noteServerDate(new Date(Date.now()).toUTCString()) // back to no offset for the next test
  vi.useRealTimers()
})

describe('useLiveClock', () => {
  it('is one clock for every user, ticks each second and stops while the tab is hidden', async () => {
    const scope = effectScope()
    const [a, b] = scope.run(() => [useLiveClock(), useLiveClock()])!
    expect(a).toBe(b)
    const start = a.value
    await vi.advanceTimersByTimeAsync(1000)
    expect(a.value - start).toBe(1000)

    hidden = true
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(5000)
    expect(a.value - start).toBe(1000)

    hidden = false
    document.dispatchEvent(new Event('visibilitychange'))
    expect(a.value - start).toBe(6000)
    scope.stop()
  })

  it('corrects its offset from a server Date header and ignores sub-second noise', () => {
    const scope = effectScope()
    const now = scope.run(() => useLiveClock())!
    // The server is 90 s ahead; its header is cut to the second, so the clock lands within that second.
    noteServerDate(new Date(Date.now() + 90_000).toUTCString())
    for (const offset of [now.value - Date.now(), serverNow() - Date.now()]) {
      expect(offset).toBeGreaterThanOrEqual(90_000)
      expect(offset).toBeLessThan(91_000)
    }

    const before = serverNow() - Date.now()
    noteServerDate(new Date(Date.now() + 90_400).toUTCString())
    noteServerDate('not a date')
    noteServerDate(undefined)
    expect(serverNow() - Date.now()).toBe(before)
    scope.stop()
  })
})
