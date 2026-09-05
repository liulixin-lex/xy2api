import { describe, expect, it } from 'vitest'

import { accountLongContextOverrideHasNoEffect } from '../longContextBilling'

const group = (id: number, enabled: boolean) => ({
  id,
  long_context_pricing_enabled: enabled,
}) as any

describe('accountLongContextOverrideHasNoEffect', () => {
  it('hides the account override when every selected group enables tier pricing', () => {
    expect(accountLongContextOverrideHasNoEffect(
      [1, 2],
      [group(1, true), group(2, true)]
    )).toBe(true)
  })

  it('keeps the account override when any selected group disables tier pricing', () => {
    expect(accountLongContextOverrideHasNoEffect(
      [1, 2],
      [group(1, true), group(2, false)]
    )).toBe(false)
  })

  it('keeps the account override when selection is empty or group data is incomplete', () => {
    expect(accountLongContextOverrideHasNoEffect([], [group(1, true)])).toBe(false)
    expect(accountLongContextOverrideHasNoEffect([1, 2], [group(1, true)])).toBe(false)
  })

  it('hides the account override for selected-model scopes because the list is authoritative', () => {
    const selected = { ...group(1, false), long_context_pricing_scope: 'selected' }
    expect(accountLongContextOverrideHasNoEffect([1], [selected])).toBe(true)
  })
})
