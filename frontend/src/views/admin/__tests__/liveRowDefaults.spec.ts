import { describe, expect, it } from 'vitest'

import { groupUsageOrDefault, userUsageOrDefault, withAccountCounts } from '../liveRowDefaults'

describe('live row defaults', () => {
  it('gives a user missing from the usage batch explicit zero usage, and fills the parts a partial answer leaves out', () => {
    expect(userUsageOrDefault(7, undefined)).toEqual({ user_id: 7, today_actual_cost: 0, total_actual_cost: 0, by_platform: [] })
    expect(userUsageOrDefault(7, { user_id: 7, today_actual_cost: 1.5 })).toEqual({
      user_id: 7, today_actual_cost: 1.5, total_actual_cost: 0, by_platform: []
    })
  })

  it('gives a group the account counts the list omits once they drop to 0', () => {
    expect(withAccountCounts({ id: 1, name: 'g', account_count: 3 })).toEqual({
      id: 1, name: 'g', account_count: 3, active_account_count: 0, rate_limited_account_count: 0
    })
    expect(withAccountCounts({ id: 1, rate_limited_account_count: 2 })).toMatchObject({ account_count: 0, rate_limited_account_count: 2 })
  })

  it('gives a group missing from the usage summary zero costs', () => {
    expect(groupUsageOrDefault(undefined)).toEqual({ today_cost: 0, yesterday_cost: 0, total_cost: 0 })
    expect(groupUsageOrDefault({ today_cost: 1, total_cost: 5 })).toEqual({ today_cost: 1, yesterday_cost: 0, total_cost: 5 })
  })
})
