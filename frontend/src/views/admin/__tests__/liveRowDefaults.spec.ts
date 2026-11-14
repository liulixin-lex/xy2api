import { describe, expect, it } from 'vitest'

import { userUsageOrDefault } from '../liveRowDefaults'

describe('live row defaults', () => {
  it('gives a user missing from the usage batch explicit zero usage, and fills the parts a partial answer leaves out', () => {
    expect(userUsageOrDefault(7, undefined)).toEqual({ user_id: 7, today_actual_cost: 0, total_actual_cost: 0, by_platform: [] })
    expect(userUsageOrDefault(7, { user_id: 7, today_actual_cost: 1.5 })).toEqual({
      user_id: 7, today_actual_cost: 1.5, total_actual_cost: 0, by_platform: []
    })
  })
})
