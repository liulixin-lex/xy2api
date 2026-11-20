import { describe, expect, it } from 'vitest'

import { accountColumnLayout } from '../accountColumns'

const DEFAULTS = ['select', 'status', 'schedulable', 'groups', 'sq_recent', 'iq_check', 'capacity', 'upstream_billing_rate', 'today_stats', 'actions']

describe('accountColumnLayout (12.5)', () => {
  it('fits the ten default columns at 1440 with the navigation folded, a 15px scrollbar gutter included', () => {
    const layout = accountColumnLayout(DEFAULTS, 1287)
    expect(layout.roomy).toBe(false)
    // Today's stats take 148px for exact amounts (mobile list plan): 1280px, still inside 1287.
    expect(layout.min).toBe(1280)
    expect(layout.widths.upstream_billing_rate).toBe(88)
    // At 1366 with the navigation open the same table scrolls sideways.
    expect(accountColumnLayout(DEFAULTS, 1046).min).toBeGreaterThan(1046)
  })

  it('switches to the roomy widths from 1454px and shares what is left only between the content columns', () => {
    expect(accountColumnLayout(DEFAULTS, 1453).roomy).toBe(false)
    expect(accountColumnLayout(DEFAULTS, 1454).roomy).toBe(true)
    // 1920 with the navigation open, a 15px gutter: 1583 - 1222 - 232 = 129px for six content columns.
    const { roomy, widths } = accountColumnLayout(DEFAULTS, 1583)
    expect(roomy).toBe(true)
    expect([widths.select, widths.schedulable, widths.upstream_billing_rate, widths.actions]).toEqual([40, 60, 96, 104])
    expect(widths.status - 140).toBeCloseTo(129 / 6)
    expect(widths.capacity - 154).toBeCloseTo(129 / 6)
  })

  it('keeps the name column between 176 and 232px from the compact minimum to past the switch (12.12-1)', () => {
    // The name column takes what the others leave of the table, which is never narrower than its minimum.
    const name = (width: number) => {
      const { min, widths } = accountColumnLayout(DEFAULTS, width)
      return +(Math.max(width, min) - Object.values(widths).reduce((total, w) => total + w, 0)).toFixed(3)
    }
    expect([1200, 1280, 1300, 1453, 1454, 1600].map(name)).toEqual([176, 176, 196, 232, 232, 232])
    // Compact past the cap, the room goes to the six content columns only: 1453 - 1104 - 232 = 117px.
    const { roomy, widths } = accountColumnLayout(DEFAULTS, 1453)
    expect(roomy).toBe(false)
    expect([widths.select, widths.schedulable, widths.upstream_billing_rate, widths.actions]).toEqual([36, 52, 88, 76])
    expect(widths.groups - 176).toBeCloseTo(117 / 6)
  })

  it('gives an optional column its share, except the account rate', () => {
    const { widths } = accountColumnLayout([...DEFAULTS, 'usage', 'rate_multiplier'], 1800)
    expect(widths.rate_multiplier).toBe(96)
    expect(widths.usage - 240).toBeCloseTo(widths.status - 140)
    expect(widths.usage).toBeGreaterThan(240)
  })

  // Mobile list plan: a phone's identity column comes from CSS, its "⋯" column is 44px; touch widens the check box.
  it('on a phone gives the actions 44px and leaves the name to CSS; touch makes the check box 44px', () => {
    const PHONE = ['select', 'status', 'schedulable', 'groups', 'sq_recent', 'actions']
    const desk = accountColumnLayout(PHONE, 0)
    const phone = accountColumnLayout(PHONE, 0, { phone: true, touch: true })
    expect(phone.widths.actions).toBe(44)
    expect(phone.widths.select).toBe(44)
    expect(phone.columns).toBe(desk.columns - 76 + 44 - 36 + 44)
    expect(phone.min - phone.columns).toBe(176)
    // A mouse on a phone-sized window keeps the 36px check box.
    expect(accountColumnLayout(PHONE, 0, { phone: true }).widths.select).toBe(36)
  })
})
