import { describe, expect, it } from 'vitest'
import { accountBilled } from '../usageRow'

describe('accountBilled', () => {
  const legacy = { total_cost: 0.02, account_stats_cost: 0.01, account_rate_multiplier: 0.5 }

  it('takes the procurement estimate as it is: the account rate is already in it', () => {
    expect(accountBilled({ ...legacy, supplier_cost_estimate: 0.003 })).toBe(0.003)
    expect(accountBilled({ ...legacy, supplier_cost_estimate: 0 })).toBe(0)
  })

  it('falls back to the legacy cost × account rate when the estimate is missing', () => {
    expect(accountBilled({ ...legacy, supplier_cost_estimate: null })).toBe(0.005)
    expect(accountBilled({ total_cost: 0.02, account_rate_multiplier: 0.5 })).toBe(0.01)
  })
})
