// Browser acceptance (step 1), item 1: the admin usage list asks without an exact count; its total is then
// "this page's end + 1" while more rows follow, a lower bound that must not be shown as a count.
import { describe, expect, it } from 'vitest'
import { usageTotalIsExact } from '../usageTotal'

describe('usageTotalIsExact', () => {
  const none = {}
  it('reads "this page\'s end + 1" as a lower bound', () => {
    expect(usageTotalIsExact(none, { page: 1, pageSize: 20, rows: 20, total: 21 })).toBe(false)
    expect(usageTotalIsExact(none, { page: 3, pageSize: 20, rows: 20, total: 61 })).toBe(false)
  })
  it('takes the last page\'s total as exact', () => {
    expect(usageTotalIsExact(none, { page: 1, pageSize: 20, rows: 7, total: 7 })).toBe(true)
    expect(usageTotalIsExact(none, { page: 2, pageSize: 20, rows: 20, total: 40 })).toBe(true)
    expect(usageTotalIsExact(none, { page: 1, pageSize: 20, rows: 0, total: 0 })).toBe(true)
  })
  it('trusts the count when a user, an API key or an account narrows the list (the server counts then)', () => {
    for (const filters of [{ user_id: 7 }, { api_key_id: 11 }, { account_id: 1 }]) {
      expect(usageTotalIsExact(filters, { page: 1, pageSize: 20, rows: 20, total: 21 })).toBe(true)
    }
  })
  it('never takes an empty page past the first as a count (its total is only its offset)', () => {
    expect(usageTotalIsExact(none, { page: 3, pageSize: 20, rows: 0, total: 40 })).toBe(false)
    expect(usageTotalIsExact({ user_id: 7 }, { page: 3, pageSize: 20, rows: 0, total: 6 })).toBe(false)
  })
})
