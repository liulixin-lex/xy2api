import { beforeEach, describe, expect, it } from 'vitest'
import { ref } from 'vue'
import { mergeColumnOrder, useColumnOrder } from '../useColumnOrder'

const STORAGE_KEY = 'test-column-order'

describe('mergeColumnOrder', () => {
  it('follows the saved order and ignores unknown or repeated keys', () => {
    expect(mergeColumnOrder(['a', 'b', 'c'], ['c', 'gone', 'a', 'c', 'b'])).toEqual(['c', 'a', 'b'])
  })

  it('puts columns the saved order does not know right after their default predecessor', () => {
    expect(mergeColumnOrder(['a', 'b', 'new', 'c'], ['c', 'b', 'a'])).toEqual(['c', 'b', 'new', 'a'])
    expect(mergeColumnOrder(['first', 'a', 'b'], ['b', 'a'])).toEqual(['first', 'b', 'a'])
  })

  it('returns the default order when nothing is saved', () => {
    expect(mergeColumnOrder(['a', 'b'], [])).toEqual(['a', 'b'])
  })
})

describe('useColumnOrder', () => {
  beforeEach(() => localStorage.clear())

  it('saves a dragged order, restores it on the next visit and can reset it', () => {
    const defaults = ref(['id', 'status', 'last_used_at'])
    const first = useColumnOrder(STORAGE_KEY, defaults)
    expect(first.orderedKeys.value).toEqual(['id', 'status', 'last_used_at'])
    expect(first.isCustomized.value).toBe(false)

    first.setOrder(['last_used_at', 'id', 'status'])
    expect(first.orderedKeys.value).toEqual(['last_used_at', 'id', 'status'])
    expect(first.isCustomized.value).toBe(true)

    const next = useColumnOrder(STORAGE_KEY, defaults)
    expect(next.orderedKeys.value).toEqual(['last_used_at', 'id', 'status'])

    next.resetOrder()
    expect(next.orderedKeys.value).toEqual(['id', 'status', 'last_used_at'])
    expect(next.isCustomized.value).toBe(false)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
  })

  it('follows columns that appear or disappear, such as mode-dependent ones', () => {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(['status', 'id']))
    const defaults = ref(['id', 'status'])
    const { orderedKeys } = useColumnOrder(STORAGE_KEY, defaults)
    defaults.value = ['id', 'groups', 'status']
    expect(orderedKeys.value).toEqual(['status', 'id', 'groups'])
  })

  it('falls back to the default order when the saved value is malformed', () => {
    localStorage.setItem(STORAGE_KEY, '{not json')
    const { orderedKeys } = useColumnOrder(STORAGE_KEY, ref(['a', 'b']))
    expect(orderedKeys.value).toEqual(['a', 'b'])
  })
})
