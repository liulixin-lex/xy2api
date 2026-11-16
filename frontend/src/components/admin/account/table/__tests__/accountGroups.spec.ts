import { describe, expect, it } from 'vitest'

import { groupDotState, orderGroups, splitPills, worstDotState, type AccountQualityView } from '../accountGroups'

const quality = (patch: Partial<AccountQualityView> = {}): AccountQualityView => ({
  group_status: {},
  stop: null,
  rear: [],
  trial: [],
  exemptions: [],
  ...patch
})

describe('group dot state', () => {
  // H.11: the group's identity comes from the summary's group_status; a group it does not name has no data.
  it('reads each identity of the group: rear, trial, pass, unknown, and unknown for a group not named', () => {
    const q = quality({ group_status: { 1: 'rear', 2: 'trial', 3: 'pass', 4: 'unknown' } })
    expect([1, 2, 3, 4, 5].map((id) => groupDotState(q, id))).toEqual(['back', 'trial', 'ok', 'unknown', 'unknown'])
  })

  it('takes the first match in the order stopped, exempt, then the group\'s identity', () => {
    const all = quality({
      group_status: { 1: 'rear' },
      stop: { source: 'auto', reason: 'rate_limit', until: null },
      exemptions: [{ group_id: 1, expires_at: null, reason: '' }]
    })
    expect(groupDotState(all, 1)).toBe('stopped')
    expect(groupDotState({ ...all, stop: null }, 1)).toBe('exempt')
    expect(groupDotState({ ...all, stop: null, exemptions: [] }, 1)).toBe('back')
    // An exemption for every group (group 0) covers a trial too; another group's exemption does not.
    expect(groupDotState(quality({ group_status: { 7: 'trial' }, exemptions: [{ group_id: 0, expires_at: null, reason: '' }] }), 7)).toBe('exempt')
    expect(groupDotState(quality({ group_status: { 7: 'trial' }, exemptions: [{ group_id: 2, expires_at: null, reason: '' }] }), 7)).toBe('trial')
  })

  it('shows the worst state on the "+N" pill: stopped, back, trial, unknown, exempt, ok', () => {
    expect(worstDotState(['ok', 'exempt', 'unknown'])).toBe('unknown')
    expect(worstDotState(['ok', 'unknown', 'trial'])).toBe('trial')
    expect(worstDotState(['trial', 'back', 'unknown'])).toBe('back')
    expect(worstDotState(['back', 'stopped'])).toBe('stopped')
    expect(worstDotState([])).toBeNull()
  })
})

describe('group pills', () => {
  it('shows up to nine pills, the ninth as "+N" once there are more', () => {
    const nine = Array.from({ length: 9 }, (_, i) => i)
    expect(splitPills(nine)).toEqual({ shown: nine, rest: [] })
    const twelve = Array.from({ length: 12 }, (_, i) => i)
    expect(splitPills(twelve).shown).toHaveLength(8)
    expect(splitPills(twelve).rest).toEqual([8, 9, 10, 11])
  })

  it('keeps the groups page order', () => {
    const groups = [{ id: 3, sort_order: 2 }, { id: 1, sort_order: 2 }, { id: 9, sort_order: 0 }]
    expect(orderGroups(groups).map((group) => group.id)).toEqual([9, 1, 3])
  })
})
