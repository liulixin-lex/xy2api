import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountCapacityCell from '../AccountCapacityCell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
    })
  }
})

const mountCell = (account: Record<string, unknown>) =>
  mount(AccountCapacityCell, {
    attachTo: document.body,
    props: { account: { id: 1, platform: 'openai', type: 'apikey', concurrency: 100, current_concurrency: 8, ...account } as never }
  })
const text = (el: { text: () => string }) => el.text().replace(/\s+/g, '')
/** The chip's v-tip, as the shared tip shows it on hover. */
const tipOf = (el: Element) => {
  vi.useFakeTimers()
  el.dispatchEvent(new Event('mouseenter'))
  vi.advanceTimersByTime(400)
  vi.useRealTimers()
  const tip = document.getElementById('sq-tip')
  const shown = tip && !tip.hidden ? tip.textContent : ''
  el.dispatchEvent(new Event('mouseleave'))
  return shown
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('AccountCapacityCell group concurrency', () => {
  it('adds the group concurrency as a third number with a tip when the list is filtered to a group', () => {
    const wrapper = mountCell({ current_group_concurrency: 3 })
    const chip = wrapper.get('[data-cap="concurrency"]')
    expect(wrapper.get('[data-test="capacity-group-current"]').text()).toBe('3')
    expect(text(chip)).toBe('8/100/3')
    expect(tipOf(chip.element)).toBe('admin.accounts.capacity.concurrencyGroup:{"current":8,"max":100,"group":3}')
  })

  it('shows zero for an idle account in the selected group', () => {
    expect(mountCell({ current_group_concurrency: 0 }).get('[data-test="capacity-group-current"]').text()).toBe('0')
  })

  it('keeps two numbers without a tip when no group is selected', () => {
    for (const account of [{}, { current_group_concurrency: null }]) {
      const wrapper = mountCell(account)
      expect(wrapper.find('[data-test="capacity-group-current"]').exists()).toBe(false)
      const chip = wrapper.get('[data-cap="concurrency"]')
      expect(text(chip)).toBe('8/100')
      expect(tipOf(chip.element)).toBe('')
      wrapper.unmount()
    }
  })
})

// The same lines as before (7e-14), filled by how much of each limit is in use (12.4).
describe('AccountCapacityCell look', () => {
  it('keeps the same lines, without icons; labels name them when there are several', () => {
    const wrapper = mountCell({
      platform: 'anthropic', type: 'oauth', current_concurrency: 3, concurrency: 20,
      window_cost_limit: 30, current_window_cost: 4.2, max_sessions: 5, active_sessions: 2, base_rpm: 60, current_rpm: 12
    })
    expect(wrapper.findAll('[data-cap]').map((chip) => chip.attributes('data-cap'))).toEqual(['concurrency', 'window_cost', 'sessions', 'rpm'])
    expect(wrapper.find('svg').exists()).toBe(false)
    expect(wrapper.findAll('.cap__l').map((label) => label.text())).toEqual([
      'admin.accounts.capacity.labels.concurrency', 'admin.accounts.capacity.labels.windowCost', 'admin.accounts.capacity.labels.sessions', 'admin.accounts.capacity.labels.rpm'
    ])
    // A whole amount drops ".00"; the tip has the exact amounts.
    expect(text(wrapper.get('[data-cap="window_cost"] .cap__v'))).toBe('$4.20/$30')
    expect(tipOf(wrapper.get('[data-cap="window_cost"]').element)).toBe('admin.accounts.capacity.windowCost.normal\n$4.20 / $30.00')
    expect(text(wrapper.get('[data-cap="rpm"]'))).toContain('[T]')
  })

  it('fills each line by use, on the thresholds of the badges before the redesign; nothing in use stays neutral (12.4)', () => {
    const tone = (account: Record<string, unknown>, key: string) =>
      mountCell(account).get(`[data-cap="${key}"]`).classes().find((cls) => cls.startsWith('cap__chip--'))
    // Concurrency: yellow as soon as anything runs, red when full.
    expect([0, 1, 99, 100].map((n) => tone({ current_concurrency: n }, 'concurrency'))).toEqual(
      ['cap__chip--neutral', 'cap__chip--yellow', 'cap__chip--yellow', 'cap__chip--red']
    )
    // Window cost (limit $30, reserve $10 by default): green, yellow from 80%, orange at the limit, red past the reserve.
    const anthropic = { platform: 'anthropic', type: 'oauth', window_cost_limit: 30 }
    expect([0, 1, 24, 30, 40].map((cost) => tone({ ...anthropic, current_window_cost: cost }, 'window_cost'))).toEqual(
      ['cap__chip--neutral', 'cap__chip--green', 'cap__chip--yellow', 'cap__chip--orange', 'cap__chip--red']
    )
    // RPM (base 50, buffer 10 by default): tiered turns red past the buffer; sticky-exempt stops at orange.
    const rpm = { ...anthropic, window_cost_limit: 0, base_rpm: 50 }
    expect([40, 50, 60].map((n) => tone({ ...rpm, current_rpm: n }, 'rpm'))).toEqual(['cap__chip--yellow', 'cap__chip--orange', 'cap__chip--red'])
    expect(tone({ ...rpm, rpm_strategy: 'sticky_exempt', current_rpm: 60 }, 'rpm')).toBe('cap__chip--orange')
    // Sessions and quotas: red at the limit, yellow from 80%.
    expect([0, 3, 4, 5].map((n) => tone({ ...anthropic, window_cost_limit: 0, max_sessions: 5, active_sessions: n }, 'sessions'))).toEqual(
      ['cap__chip--neutral', 'cap__chip--green', 'cap__chip--yellow', 'cap__chip--red']
    )
    expect([0, 10, 80, 100].map((used) => tone({ quota_daily_limit: 100, quota_daily_used: used }, 'quota_daily'))).toEqual(
      ['cap__chip--neutral', 'cap__chip--green', 'cap__chip--yellow', 'cap__chip--red']
    )
    // A single line carries no label.
    expect(mountCell({}).find('.cap__l').exists()).toBe(false)
  })
})

// 7i-1: amounts from $1,000 take the three-digit abbreviation; a value the column cuts off is marked and told in full.
describe('AccountCapacityCell fit', () => {
  it('abbreviates an amount from $1,000, the tip keeping the exact one', () => {
    const wrapper = mountCell({ platform: 'anthropic', type: 'oauth', window_cost_limit: 1500, current_window_cost: 1234.5 })
    expect(text(wrapper.get('[data-cap="window_cost"] .cap__v'))).toBe('$1.23K/$1.50K')
    expect(tipOf(wrapper.get('[data-cap="window_cost"]').element)).toBe('admin.accounts.capacity.windowCost.normal\n$1234.50 / $1500.00')
  })

  it('tells the whole value in the tip only when the column cuts it off', () => {
    const wrapper = mountCell({ platform: 'anthropic', type: 'oauth', base_rpm: 1200, current_rpm: 1100, rpm_strategy: 'tiered' })
    const chip = wrapper.get('[data-cap="rpm"]')
    const tip = 'admin.accounts.capacity.rpm.tieredWarning'
    expect(tipOf(chip.element)).toBe(tip)
    const value = chip.get('.cap__v').element
    Object.defineProperty(value, 'scrollWidth', { value: 80, configurable: true })
    Object.defineProperty(value, 'clientWidth', { value: 60, configurable: true })
    expect(tipOf(chip.element)).toBe(`${tip}\n1100 / 1200 [T]`)
  })
})
