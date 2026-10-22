import { describe, expect, it, vi } from 'vitest'
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
    props: { account: { id: 1, platform: 'openai', type: 'apikey', concurrency: 100, current_concurrency: 8, ...account } as never }
  })

describe('AccountCapacityCell group concurrency', () => {
  it('adds the group concurrency as a green third number with a tooltip when the list is filtered to a group', () => {
    const wrapper = mountCell({ current_group_concurrency: 3 })
    const badge = wrapper.get('[data-test="capacity-group-current"]')

    expect(badge.text()).toBe('3')
    expect(badge.classes()).toEqual(expect.arrayContaining(['text-emerald-600', 'dark:text-emerald-400']))
    expect(badge.element.parentElement!.textContent!.replace(/\s+/g, '')).toBe('8/100/3')
    expect(badge.element.parentElement!.getAttribute('title')).toBe(
      'admin.accounts.capacity.concurrencyGroup:{"current":8,"max":100,"group":3}'
    )
  })

  it('shows zero for an idle account in the selected group', () => {
    expect(mountCell({ current_group_concurrency: 0 }).get('[data-test="capacity-group-current"]').text()).toBe('0')
  })

  it('keeps the two-number badge without a tooltip when no group is selected', () => {
    for (const account of [{}, { current_group_concurrency: null }]) {
      const wrapper = mountCell(account)
      expect(wrapper.find('[data-test="capacity-group-current"]').exists()).toBe(false)
      const badge = wrapper.get('span.font-mono').element.parentElement!
      expect(badge.textContent!.replace(/\s+/g, '')).toBe('8/100')
      expect(badge.hasAttribute('title')).toBe(false)
    }
  })
})
