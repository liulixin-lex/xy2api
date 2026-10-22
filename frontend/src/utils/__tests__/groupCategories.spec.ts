import { describe, expect, it } from 'vitest'
import { groupCategoryErrorMessage, normalizeCategoryName, validateCategoryDraft } from '../groupCategories'

const t = (key: string) => `t:${key}`

describe('group category name rules', () => {
  it('trims ASCII and full-width spaces', () => {
    expect(normalizeCategoryName('　 公开分组 \t')).toBe('公开分组')
  })

  it('accepts 32 characters and rejects 33, counting code points', () => {
    expect(validateCategoryDraft([{ name: '分'.repeat(32) }])).toBeNull()
    expect(validateCategoryDraft([{ name: '分'.repeat(33) }])).toEqual({ key: 'nameTooLong', index: 0 })
    expect(validateCategoryDraft([{ name: '😀'.repeat(32) }])).toBeNull()
  })

  it('rejects empty names, control characters and case-insensitive duplicates', () => {
    expect(validateCategoryDraft([{ name: 'A' }, { name: '　' }])).toEqual({ key: 'nameRequired', index: 1 })
    expect(validateCategoryDraft([{ name: 'a\u0007b' }])).toEqual({ key: 'nameInvalid', index: 0 })
    expect(validateCategoryDraft([{ name: 'a\u0085b' }])).toEqual({ key: 'nameInvalid', index: 0 })
    expect(validateCategoryDraft([{ name: 'Public' }, { name: ' public ' }])).toEqual({ key: 'nameDuplicate', index: 1 })
  })

  it('allows at most 50 categories', () => {
    const names = Array.from({ length: 50 }, (_, index) => ({ name: `c${index}` }))
    expect(validateCategoryDraft(names)).toBeNull()
    expect(validateCategoryDraft([...names, { name: 'extra' }])).toEqual({ key: 'limitReached', index: -1 })
  })
})

describe('group category error messages', () => {
  it('localizes known codes and falls back to the server message', () => {
    expect(groupCategoryErrorMessage({ reason: 'GROUP_CATEGORY_NAME_DUPLICATE', message: 'dup' }, t, 'fallback')).toBe('t:admin.groupCategories.nameDuplicate')
    expect(groupCategoryErrorMessage({ reason: 'GROUP_CATEGORY_LIMIT' }, t, 'fallback')).toBe('t:admin.groupCategories.limitReached')
    expect(groupCategoryErrorMessage({ reason: 'GROUP_CATEGORY_NAME_INVALID', message: 'bad name' }, t, 'fallback')).toBe('bad name')
    expect(groupCategoryErrorMessage({}, t, 'fallback')).toBe('fallback')
  })
})
