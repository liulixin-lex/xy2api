import { describe, expect, it } from 'vitest'
import { quoteRechargeBonus, type RechargeBonusTier } from '@/utils/rechargeBonus'
import { nearestPayableAmount } from '../payableAmount'

function payBaseOf(tiers: RechargeBonusTier[], mode: 'bonus' | 'discount' = 'discount') {
  return (amount: number) => quoteRechargeBonus(tiers, amount, { mode, currencyDigits: 2 }).payBase
}
const starts = (tiers: RechargeBonusTier[]) => tiers.map(tier => tier.min_amount)

describe('nearestPayableAmount', () => {
  it('is the limit itself when nothing discounts the amount', () => {
    const tiers = [{ min_amount: 200, bonus_percent: 3 }]
    const quote = payBaseOf(tiers, 'bonus')
    expect(nearestPayableAmount(20, 'up', [{ min: 50, max: 2000 }], quote, starts(tiers))).toBe(50)
    expect(nearestPayableAmount(3000, 'down', [{ min: 50, max: 2000 }], quote, starts(tiers))).toBe(2000)
  })

  it('works a discounted minimum and maximum back to the amount the user types, to the cent', () => {
    const tiers = [{ min_amount: 1, bonus_percent: 10 }]
    const quote = payBaseOf(tiers)
    // 111.10 pays 99.99, 111.11 pays 100.00
    expect(nearestPayableAmount(100, 'up', [{ min: 100, max: 0 }], quote, starts(tiers))).toBe(111.11)
    // 2222.22 pays 2000.00, 2222.23 pays 2000.01
    expect(nearestPayableAmount(3000, 'down', [{ min: 0, max: 2000 }], quote, starts(tiers))).toBe(2222.22)
  })

  it('stays on the near side when a tier makes the pay base step down', () => {
    const tiers = [{ min_amount: 1000, bonus_percent: 10 }]
    const quote = payBaseOf(tiers)
    // 980 pays 980 > 950; the nearest payable amount below it is 950 (1000 to 1055.55 would pay ≤ 950 too, but is above)
    expect(nearestPayableAmount(980, 'down', [{ min: 0, max: 950 }], quote, starts(tiers))).toBe(950)
    // 1000 pays 900 < 960; going up, 1066.67 is the first amount that pays 960.00
    expect(nearestPayableAmount(1000, 'up', [{ min: 960, max: 0 }], quote, starts(tiers))).toBe(1066.67)
  })

  it('takes the nearest of several methods and gives up when there is none that way', () => {
    const tiers = [{ min_amount: 1, bonus_percent: 50 }]
    const quote = payBaseOf(tiers)
    // 159.99 pays 80.00 (79.995 rounds up), 159.98 pays 79.99
    expect(nearestPayableAmount(100, 'up', [{ min: 300, max: 0 }, { min: 80, max: 90 }], quote, starts(tiers))).toBe(159.99)
    expect(nearestPayableAmount(300, 'down', [{ min: 250, max: 260 }], quote, starts(tiers))).toBeNull()
  })
})
