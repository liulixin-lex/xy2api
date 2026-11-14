import { describe, expect, it } from 'vitest'

import { groupDotState, groupPillText, isLatinPillText, orderGroups, splitPills, worstDotState, type AccountQualityView } from '../accountGroups'

const quality = (patch: Partial<AccountQualityView> = {}): AccountQualityView => ({
  status: 'normal',
  stop: null,
  substandard: [],
  exemptions: [],
  ...patch
})
const back = (groupId: number, model: string, effective = true) => ({ group_id: groupId, model, dims: ['ttfo' as const], until: '', other_group: false, exempt: false, effective })

describe('group dot state', () => {
  it('derives the five states', () => {
    expect(groupDotState(quality({ stop: { source: 'manual', reason: 'manual_off', until: null } }), 1)).toBe('stopped')
    expect(groupDotState(quality({ exemptions: [{ group_id: 1, expires_at: null, reason: '' }] }), 1)).toBe('exempt')
    expect(groupDotState(quality({ substandard: [back(1, 'gpt-6')] }), 1)).toBe('back')
    expect(groupDotState(quality({ status: 'observing' }), 1)).toBe('observe')
    expect(groupDotState(quality(), 1)).toBe('ok')
  })

  it('takes the first match in the order stopped, exempt, back, observing', () => {
    const all = quality({
      status: 'observing',
      stop: { source: 'auto', reason: 'rate_limit', until: null },
      substandard: [back(1, 'gpt-6')],
      exemptions: [{ group_id: 1, expires_at: null, reason: '' }]
    })
    expect(groupDotState(all, 1)).toBe('stopped')
    expect(groupDotState({ ...all, stop: null }, 1)).toBe('exempt')
    expect(groupDotState({ ...all, stop: null, exemptions: [] }, 1)).toBe('back')
  })

  it('counts an exemption for every group (group 0) and only this group\'s substandard models', () => {
    expect(groupDotState(quality({ exemptions: [{ group_id: 0, expires_at: null, reason: '' }] }), 7)).toBe('exempt')
    expect(groupDotState(quality({ substandard: [back(2, 'gpt-6')] }), 1)).toBe('ok')
  })

  // 7f-2, spec 9.6: a substandard record not in effect (quality scheduling off, a stale view, the group's policy off)
  // puts nobody at the back: the dot reads as if it were not there.
  it('is back only for a record in effect', () => {
    expect(groupDotState(quality({ substandard: [back(1, 'gpt-6', false)] }), 1)).toBe('ok')
    expect(groupDotState(quality({ substandard: [back(1, 'gpt-6', false)], status: 'observing' }), 1)).toBe('observe')
    expect(groupDotState(quality({ substandard: [back(1, 'gpt-6', false), back(1, 'gpt-6.1-sol')] }), 1)).toBe('back')
  })

  it('is back as soon as any one model of the group is substandard', () => {
    expect(groupDotState(quality({ substandard: [back(1, 'gpt-6.1-sol')] }), 1)).toBe('back')
  })

  it('shows the worst state on the "+N" pill', () => {
    expect(worstDotState(['ok', 'exempt', 'observe'])).toBe('observe')
    expect(worstDotState(['ok', 'back', 'observe'])).toBe('back')
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

describe('pill text', () => {
  it('uses the alias when there is one', () => {
    expect(groupPillText({ name: 'OpenAI 主力池', alias: '主力' })).toBe('主力')
  })

  it('takes the first two Chinese characters, else the first three letters and digits', () => {
    expect(groupPillText({ name: 'GPT 主力 备用池', alias: '' })).toBe('主力')
    expect(groupPillText({ name: 'Gemini Ultra' })).toBe('Gem')
    expect(groupPillText({ name: 'g-4 pro' })).toBe('g4p')
    expect(groupPillText({ name: '  ', alias: '  ' })).toBe('  ')
  })

  it('sets text made of ASCII only at the small size', () => {
    expect(isLatinPillText('Gem')).toBe(true)
    expect(isLatinPillText('主力')).toBe(false)
    expect(isLatinPillText('A主')).toBe(false)
  })
})
