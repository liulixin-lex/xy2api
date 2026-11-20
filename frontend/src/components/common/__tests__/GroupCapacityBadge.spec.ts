import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupCapacityBadge from '../GroupCapacityBadge.vue'

const baseProps = { concurrencyUsed: 5, concurrencyMax: 600, sessionsUsed: 0, sessionsMax: 0, rpmUsed: 0, rpmMax: 0 }

describe('GroupCapacityBadge group concurrency', () => {
  it('adds the slots held through this group as a green third number', () => {
    const wrapper = mount(GroupCapacityBadge, { props: { ...baseProps, concurrencyGroupUsed: 3 } })
    const groupUsed = wrapper.get('[data-test="capacity-group-current"]')

    expect(groupUsed.text()).toBe('3')
    expect(groupUsed.classes()).toEqual(expect.arrayContaining(['text-emerald-600', 'dark:text-emerald-400']))
    expect(groupUsed.element.parentElement!.textContent!.replace(/\s+/g, '')).toBe('5/600/3')
  })

  it('shows zero when the group holds no slots', () => {
    const wrapper = mount(GroupCapacityBadge, { props: { ...baseProps, concurrencyGroupUsed: 0 } })
    expect(wrapper.get('[data-test="capacity-group-current"]').text()).toBe('0')
  })

  it('keeps two numbers when the group concurrency is missing', () => {
    const wrapper = mount(GroupCapacityBadge, { props: baseProps })
    expect(wrapper.find('[data-test="capacity-group-current"]').exists()).toBe(false)
    expect(wrapper.get('span.font-mono').element.parentElement!.textContent!.replace(/\s+/g, '')).toBe('5/600')
  })

  // Mobile list plan, item 19: no text under 12px.
  it('draws its badges at 12px', () => {
    const wrapper = mount(GroupCapacityBadge, { props: { ...baseProps, sessionsMax: 10, rpmMax: 100 } })
    const badges = wrapper.findAll('span.rounded-md')
    expect(badges.length).toBeGreaterThan(0)
    for (const badge of badges) expect(badge.classes()).toContain('text-xs')
  })
})
