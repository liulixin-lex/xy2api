import { describe, expect, it } from 'vitest'

import type { SchedRecentAttempt } from '@/types/schedQuality'
import { attemptResult, enumWord, known, SCHED_CLASSES, SCHED_STATES, SCHED_VERDICTS } from '../schedQualityFormat'

const t = (key: string) => key

describe('finite enums (S8)', () => {
  it('passes known values and turns unknown ones into the fallback word, never a default verdict or a raw key', () => {
    expect(known('cooling', SCHED_STATES)).toBe('cooling')
    expect(known('future', SCHED_STATES)).toBeNull()
    expect(known('future', SCHED_VERDICTS)).toBeNull()
    expect(known(undefined, SCHED_VERDICTS)).toBeNull()
    expect(enumWord('heavy', SCHED_CLASSES, 'admin.schedQuality.class', t)).toBe('admin.schedQuality.class.heavy')
    expect(enumWord('future', SCHED_CLASSES, 'admin.schedQuality.class', t)).toBe('admin.schedQuality.unknown')

    const attempt = { outcome: 'future', detail: '' } as unknown as SchedRecentAttempt
    expect(attemptResult(attempt)).toBeNull()
    expect(attemptResult({ ...attempt, outcome: 'account_fault' })).toBe('failed')
    expect(attemptResult({ ...attempt, outcome: 'other', detail: 'client_cancel' })).toBe('canceled')
  })
})
