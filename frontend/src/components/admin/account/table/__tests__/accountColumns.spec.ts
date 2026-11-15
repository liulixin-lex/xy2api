import { describe, expect, it } from 'vitest'

import { accountColumnLayout } from '../accountColumns'

const DEFAULTS = ['select', 'status', 'schedulable', 'groups', 'sq_recent', 'iq_check', 'capacity', 'upstream_billing_rate', 'today_stats', 'actions']

describe('accountColumnLayout (12.5)', () => {
  it('fits the ten default columns at 1440 with the navigation folded, a 15px scrollbar gutter included', () => {
    const layout = accountColumnLayout(DEFAULTS, 1287)
    expect(layout.roomy).toBe(false)
    expect(layout.min).toBe(1262)
    expect(layout.widths.upstream_billing_rate).toBe(88)
    // At 1366 with the navigation open the same table scrolls sideways.
    expect(accountColumnLayout(DEFAULTS, 1046).min).toBeGreaterThan(1046)
  })

  it('switches to the roomy widths from 1438px and shares what is left only between the content columns', () => {
    expect(accountColumnLayout(DEFAULTS, 1437).roomy).toBe(false)
    // 1920 with the navigation open, a 15px gutter: 1583 - 2 - 1206 - 232 = 143px for six content columns.
    const { roomy, widths } = accountColumnLayout(DEFAULTS, 1583)
    expect(roomy).toBe(true)
    expect([widths.select, widths.schedulable, widths.upstream_billing_rate, widths.actions]).toEqual([40, 60, 96, 104])
    expect(widths.status - 140).toBeCloseTo(143 / 6)
    expect(widths.capacity - 154).toBeCloseTo(143 / 6)
  })

  it('gives an optional column its share, except the account rate', () => {
    const { widths } = accountColumnLayout([...DEFAULTS, 'usage', 'rate_multiplier'], 1800)
    expect(widths.rate_multiplier).toBe(96)
    expect(widths.usage - 240).toBeCloseTo(widths.status - 140)
    expect(widths.usage).toBeGreaterThan(240)
  })
})
