import { describe, expect, it } from 'vitest'
import { formatShareMs, formatSharePercent, isShareConflict, isShareUnavailable, sharePrioritySections } from '../shareScheduling'
import { shareMember } from '@/stores/__tests__/shareSchedulingFixtures'

describe('sharePrioritySections', () => {
  it('groups members by priority, lowest first, and marks the active one', () => {
    const sections = sharePrioritySections([
      shareMember(4, { priority: 5, expected_share: 0 }),
      shareMember(1, { priority: 1, expected_share: 0.2 }),
      shareMember(2, { priority: 1, expected_share: 0.8 }),
      shareMember(3, { priority: 2, expected_share: 0 })
    ], 1)
    expect(sections.map(s => [s.priority, s.active, s.members.map(m => m.account_id)])).toEqual([
      [1, true, [2, 1]],
      [2, false, [3]],
      [5, false, [4]]
    ])
  })

  it('orders standby members by their share once healthy', () => {
    const [section] = sharePrioritySections([
      shareMember(1, { healthy: false, expected_share: 0, share_if_healthy: 0.1 }),
      shareMember(2, { healthy: false, expected_share: 0, share_if_healthy: 0.6 })
    ], null)
    expect(section.active).toBe(false)
    expect(section.members.map(m => m.account_id)).toEqual([2, 1])
  })
})

describe('share scheduling helpers', () => {
  it('recognises conflicts and unavailable storage', () => {
    expect(isShareConflict({ status: 409 })).toBe(true)
    expect(isShareConflict({ reason: 'SHARE_SCHEDULING_VERSION_CONFLICT' })).toBe(true)
    expect(isShareConflict({ status: 400 })).toBe(false)
    expect(isShareConflict(null)).toBe(false)
    expect(isShareUnavailable({ status: 503 })).toBe(true)
    expect(isShareUnavailable({ reason: 'SHARE_SCHEDULING_UNAVAILABLE' })).toBe(true)
    expect(isShareUnavailable(undefined)).toBe(false)
  })

  it('formats percentages and durations', () => {
    expect(formatSharePercent(0.1234)).toBe('12.3%')
    expect(formatSharePercent(0.0021, 2)).toBe('0.21%')
    expect(formatSharePercent(null)).toBe('-')
    expect(formatSharePercent(Number.NaN)).toBe('-')
    expect(formatShareMs(350)).toBe('350 ms')
    expect(formatShareMs(1500)).toBe('1.5 s')
    expect(formatShareMs(90_000)).toBe('1.5 min')
    expect(formatShareMs(7_200_000)).toBe('2.0 h')
    expect(formatShareMs(undefined)).toBe('-')
  })
})
