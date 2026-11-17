import { afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { schedWord, secondsUntil } from '../schedQualityFormat'
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

describe('words for backend values', () => {
  it('reads a value from its copy table and shows 其他 for one the table does not know, never the key', () => {
    const known = new Set(['admin.schedQuality.identity.rear'])
    const t = (key: string) => (key === 'admin.schedQuality.unknown' ? '其他' : `word:${key}`)
    const te = (key: string) => known.has(key)
    expect(schedWord('identity', 'rear', t, te)).toBe('word:admin.schedQuality.identity.rear')
    expect(schedWord('identity', 'probation', t, te)).toBe('其他')
    expect(schedWord('decisions.action', 'cooldown', t, te)).toBe('其他')
  })
})
