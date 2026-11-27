import { describe, expect, it } from 'vitest'
import { accountBilled, voluntaryCompensation } from '../usageRow'
import type { AdminUsageLog } from '@/types'

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

describe('voluntaryCompensation', () => {
  const row = (adjustments: unknown[] | undefined) =>
    ({ metering_adjustment: adjustments ? { v: 1, adjustments } : undefined } as unknown as AdminUsageLog)
  const credited = (amount: number) => ({ kind: 'openai_voluntary_compensation', amount, estimated_amount: 0.004, reason: 'margin_limited' })
  const pending = (estimated_amount: number) => ({ kind: 'openai_voluntary_compensation_pending', amount: 0, estimated_amount, reason: 'supplier_cost_unknown' })

  it('reads the credited amount, not the estimate it was capped from', () => {
    expect(voluntaryCompensation(row([{ kind: 'force_cache_billing', input_to_cache_tokens: 10 }, credited(0.0025)]))).toEqual({ credited: 0.0025, pending: null })
  })

  it('reads a voucher to settle, unpriced included', () => {
    expect(voluntaryCompensation(row([pending(0.0031)]))).toEqual({ credited: 0, pending: 0.0031 })
    expect(voluntaryCompensation(row([pending(0)]))).toEqual({ credited: 0, pending: 0 })
  })

  it('has nothing to show without compensation or when the margin floor left no room', () => {
    expect(voluntaryCompensation(row(undefined))).toBeNull()
    expect(voluntaryCompensation(row([{ kind: 'cache_ttl_override' }]))).toBeNull()
    expect(voluntaryCompensation(row([credited(0)]))).toBeNull()
  })
})
