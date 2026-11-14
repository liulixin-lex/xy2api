import { describe, expect, it } from 'vitest'

import type { SchedRecentAttempt, SchedTestTicket } from '@/types/schedQuality'
import { attemptResult, enumWord, known, SCHED_CLASSES, SCHED_STATES, SCHED_VERDICTS, testResult } from '../schedQualityFormat'

const ticket = (started: boolean, why: SchedTestTicket['why']): SchedTestTicket => ({ model: 'm', test_id: started ? 't' : '', started, why })
const t = (key: string) => key

describe('testResult (B.11, K28)', () => {
  it('is ok only when a model started and none hit an internal error', () => {
    expect(testResult([ticket(true, ''), ticket(false, 'too_soon')])).toBe('ok')
    expect(testResult([ticket(true, ''), ticket(false, 'error')])).toBe('failed')
    expect(testResult([ticket(false, 'already_running'), ticket(false, 'already_running')])).toBe('testing')
    expect(testResult([ticket(false, 'already_running'), ticket(false, 'error')])).toBe('failed')
    expect(testResult([ticket(false, 'not_schedulable')])).toBe('failed')
    expect(testResult([])).toBe('failed')
  })
})

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
