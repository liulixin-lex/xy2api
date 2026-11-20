import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import DateRangePicker from '../DateRangePicker.vue'

const messages: Record<string, string> = {
  'dates.today': 'Today',
  'dates.yesterday': 'Yesterday',
  'dates.last24Hours': 'Last 24 Hours',
  'dates.last7Days': 'Last 7 Days',
  'dates.last14Days': 'Last 14 Days',
  'dates.last30Days': 'Last 30 Days',
  'dates.thisMonth': 'This Month',
  'dates.lastMonth': 'Last Month',
  'dates.startDate': 'Start Date',
  'dates.endDate': 'End Date',
  'dates.apply': 'Apply',
  'dates.selectDateRange': 'Select date range'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
    locale: ref('en')
  })
}))

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

describe('DateRangePicker', () => {
  it('uses last 24 hours as the default recognized preset', () => {
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: formatLocalDate(yesterday),
        endDate: formatLocalDate(now)
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('Last 24 Hours')
  })

  it('emits range updates with last24Hours preset when applied', async () => {
    const now = new Date()
    const today = formatLocalDate(now)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: today,
        endDate: today
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Last 24 Hours')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    const nowAfterClick = new Date()
    const yesterdayAfterClick = new Date(nowAfterClick.getTime() - 24 * 60 * 60 * 1000)
    const expectedStart = formatLocalDate(yesterdayAfterClick)
    const expectedEnd = formatLocalDate(nowAfterClick)

    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([expectedStart])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([expectedEnd])
    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: expectedStart,
        endDate: expectedEnd,
        preset: 'last24Hours'
      }
    ])
  })

  // A 320px phone: the dropdown (at most the screen less 8px each side) moves left to keep 8px from the right edge.
  it('keeps its dropdown on screen with an 8px margin', async () => {
    const width = vi.spyOn(document.documentElement, 'clientWidth', 'get').mockReturnValue(320)
    const offset = vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(304)
    const rect = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ left: 100 } as DOMRect)
    try {
      const today = formatLocalDate(new Date())
      const wrapper = mount(DateRangePicker, {
        props: { startDate: today, endDate: today },
        global: { stubs: { Icon: true } }
      })
      await wrapper.get('.date-picker-trigger').trigger('click')
      // 100 + 304 runs 92px past 312 (320 less 8).
      expect(wrapper.get('.date-picker-dropdown').attributes('style')).toContain('left: -92px')

      // Never past 8px from the left edge: a trigger at 16px moves 8px at most.
      await wrapper.get('.date-picker-trigger').trigger('click')
      rect.mockReturnValue({ left: 16 } as DOMRect)
      offset.mockReturnValue(320)
      await wrapper.get('.date-picker-trigger').trigger('click')
      expect(wrapper.get('.date-picker-dropdown').attributes('style')).toContain('left: -8px')

      // Room enough: where it was.
      await wrapper.get('.date-picker-trigger').trigger('click')
      width.mockReturnValue(1440)
      await wrapper.get('.date-picker-trigger').trigger('click')
      expect(wrapper.get('.date-picker-dropdown').attributes('style')).toBeUndefined()
      wrapper.unmount()
    } finally {
      width.mockRestore()
      offset.mockRestore()
      rect.mockRestore()
    }
  })
})
