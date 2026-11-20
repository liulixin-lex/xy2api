import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountTodayCell from '../AccountTodayCell.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

const values = (stats: unknown) => mount(AccountTodayCell, { props: { stats: stats as never } }).findAll('.today__v').map((el) => el.text())

describe('AccountTodayCell', () => {
  it('reads "—" on every line before the stats are in, not 0 and $0.00 (7d-6)', () => {
    expect(values(undefined)).toEqual(['—', '—', '—', '—'])
    expect(values(null)).toEqual(['—', '—', '—', '—'])
  })

  it('shows the stats once they are in, zeros included', () => {
    expect(values({ requests: 12_000, tokens: 313_000_000, cost: 9.995, user_cost: 0 })).toEqual(['12,000', '313M', '$10.00', '$0.00'])
  })

  // Mobile list plan (Astra review #10): amounts are exact, two decimals with thousands separators, never "$1.23K";
  // counts stay compact with the full value in the tip; a missing value is a light "—".
  it('writes amounts exact with thousands separators, counts compact, a missing value light', () => {
    const wrapper = mount(AccountTodayCell, { props: { stats: { requests: 123_456, tokens: 532_730_000, cost: 1234.56, user_cost: 12_345.678 } as never } })
    expect(wrapper.findAll('.today__v').map((el) => el.text())).toEqual(['123K', '533M', '$1,234.56', '$12,345.68'])
    expect(mount(AccountTodayCell, { props: { stats: null } }).findAll('.today__v--na')).toHaveLength(4)
    expect(wrapper.findAll('.today__v--na')).toHaveLength(0)
  })

  // Astra second re-review #3: at $100,000.00 the English label is cut. Whatever is cut, the whole cell shows every
  // label whole and every value exact on a tap and on keyboard focus, without measuring.
  it('shows every label whole and every value exact on a tap and keyboard focus, $100,000.00 included', async () => {
    const wrapper = mount(AccountTodayCell, {
      attachTo: document.body,
      props: { stats: { requests: 123_456, tokens: 532_730_000, cost: 100_000, user_cost: 100_000 } as never }
    })
    const cell = wrapper.get('[data-test="account-today"]')
    expect(cell.attributes('tabindex')).toBe('0')
    await cell.trigger('click')
    const tip = document.getElementById('sq-tip')!
    expect(tip.hidden).toBe(false)
    for (const text of ['admin.accounts.table.todayUserCost', 'admin.accounts.table.todayCost', '$100,000.00', '532,730,000', '123,456']) expect(tip.textContent).toContain(text)
    wrapper.unmount()
    // Before the stats are in there is nothing to show: no tab stop, no tip.
    const empty = mount(AccountTodayCell, { props: { stats: null } })
    expect(empty.get('[data-test="account-today"]').attributes('tabindex')).toBeUndefined()
  })
})
