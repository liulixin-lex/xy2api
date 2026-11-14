import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { secondsUntil } from '../schedQualityFormat'
import { useLiveClock } from '@/composables/useLiveClock'

afterEach(() => {
  vi.useRealTimers()
})

describe('cooling countdown', () => {
  it('counts down from until on the shared page clock without any request', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-11T12:04:00Z'))
    const scope = effectScope()
    const now = scope.run(() => useLiveClock())!
    const until = '2026-11-11T12:04:30Z'
    expect(secondsUntil(until, now.value)).toBe(30)

    await vi.advanceTimersByTimeAsync(1000)
    expect(secondsUntil(until, now.value)).toBe(29)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(secondsUntil(until, now.value)).toBe(0)
    expect(secondsUntil(null, now.value)).toBeNull()
    scope.stop()
  })
})
