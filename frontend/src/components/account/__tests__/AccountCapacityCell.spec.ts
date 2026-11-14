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

// The prototype's neutral chips, same lines as before (7e-14).
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

  it('greys a line with nothing in use and marks only a full one', () => {
    const idle = mountCell({ current_concurrency: 0 }).get('[data-cap="concurrency"]')
    expect(idle.classes()).toContain('cap__chip--zero')
    expect(idle.classes()).not.toContain('cap__chip--full')
    // 80% used is not full: still neutral.
    const busy = mountCell({ current_concurrency: 80 }).get('[data-cap="concurrency"]')
    expect(busy.classes()).not.toContain('cap__chip--zero')
    expect(busy.classes()).not.toContain('cap__chip--full')
    expect(mountCell({ current_concurrency: 100 }).get('[data-cap="concurrency"]').classes()).toContain('cap__chip--full')
    // A single line carries no label.
    expect(mountCell({}).find('.cap__l').exists()).toBe(false)
  })
})
