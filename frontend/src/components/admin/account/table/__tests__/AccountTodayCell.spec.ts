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
})
