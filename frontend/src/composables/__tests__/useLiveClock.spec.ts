import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { noteServerDate, useLiveClock } from '../useLiveClock'

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
    const offset = now.value - Date.now()
    expect(offset).toBeGreaterThanOrEqual(90_000)
    expect(offset).toBeLessThan(91_000)

    noteServerDate(new Date(Date.now() + 90_400).toUTCString())
    noteServerDate('not a date')
    noteServerDate(undefined)
    vi.advanceTimersByTime(1000)
    expect(now.value - Date.now()).toBe(offset)
    scope.stop()
  })

  it('takes the midpoint of sending and receiving as the moment the server stamped its header', () => {
    const scope = effectScope()
    const now = scope.run(() => useLiveClock())!
    // Sent at a whole second and answered 4 s later; the server, 60 s ahead, stamped it 2 s after sending.
    const sentAt = Date.now()
    vi.advanceTimersByTime(4000)
    noteServerDate(new Date(sentAt + 2000 + 60_000).toUTCString(), sentAt)
    expect(now.value - Date.now()).toBe(60_500)
    scope.stop()
  })

  it('does not keep a clock running for a caller outside any scope', async () => {
    const now = useLiveClock()
    const start = now.value
    await vi.advanceTimersByTimeAsync(5000)
    expect(now.value).toBe(start)
    expect(vi.getTimerCount()).toBe(0)
  })
})
