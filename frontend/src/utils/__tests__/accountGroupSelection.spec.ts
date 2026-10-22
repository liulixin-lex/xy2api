import { describe, expect, it } from 'vitest'
import type { Group } from '@/types'
import { mergeSelectableGroups, sameIdList } from '../accountGroupSelection'

const group = (id: number, status: 'active' | 'inactive' = 'active') => ({ id, name: `g${id}`, platform: 'openai', status }) as Group

describe('account group selection helpers', () => {
  it('adds bound inactive groups to the active list once', () => {
    const active = [group(1), group(2)]
    const account = { group_ids: [3, 1], groups: [group(3, 'inactive'), group(1), group(9, 'inactive')] }
    expect(mergeSelectableGroups(active, account).map((item) => item.id)).toEqual([1, 2, 3])
    expect(mergeSelectableGroups(active, null).map((item) => item.id)).toEqual([1, 2])
  })

  it('compares id lists in order', () => {
    expect(sameIdList([3, 1], [3, 1])).toBe(true)
    expect(sameIdList([3, 1], [1, 3])).toBe(false)
    expect(sameIdList([3, 1], [3, 1, 2])).toBe(false)
    expect(sameIdList([], [])).toBe(true)
  })
})
