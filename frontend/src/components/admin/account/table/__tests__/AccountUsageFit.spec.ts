import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { h } from 'vue'

import AccountUsageFit from '../AccountUsageFit.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }) }
})

const LINE = 15

/** jsdom has no layout: the cell is 77px tall and each window shown takes one 15px line. */
function mountWith(count: number) {
  const wrapper = mount(AccountUsageFit, {
    attachTo: document.body,
    slots: {
      default: () => Array.from({ length: count }, (_, i) => h('div', { 'data-usage-window': '' }, [`W${i + 1} ${10 * (i + 1)}%`, h('span', { 'data-usage-window-stats': '' }, ` ${i + 1}K req`)]))
    }
  })
  const box = wrapper.get('[data-test="usage-fit"]').element as HTMLElement
  const inner = box.firstElementChild as HTMLElement
  Object.defineProperty(box, 'clientHeight', { configurable: true, get: () => 77 })
  Object.defineProperty(inner, 'scrollHeight', {
    configurable: true,
    get: () => inner.querySelectorAll('[data-usage-window]:not([data-usage-folded])').length * LINE
  })
  ;(wrapper.vm as unknown as { fit: () => void }).fit()
  return wrapper
}

// The usage windows column keeps to the 78px row (7e-6).
describe('AccountUsageFit', () => {
  it('shows every window when they fit, with no "+N"', async () => {
    const wrapper = mountWith(4)
    await wrapper.vm.$nextTick()
    expect(wrapper.findAll('[data-usage-window]:not([data-usage-folded])')).toHaveLength(4)
    expect(wrapper.find('[data-test="usage-more"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('folds the last windows that do not fit, counts them in "+N" and lists every window in the tip', async () => {
    const wrapper = mountWith(7)
    await wrapper.vm.$nextTick()
    // 77px with 14px kept for "+N": four 15px lines.
    expect(wrapper.findAll('[data-usage-window]:not([data-usage-folded])').map((el) => el.text())).toEqual(['W1 10% 1K req', 'W2 20% 2K req', 'W3 30% 3K req', 'W4 40% 4K req'])
    expect(wrapper.get('[data-test="usage-more"]').text()).toBe('admin.accounts.table.usageMore:{"n":3}')

    const box = wrapper.get('[data-test="usage-fit"]').element as HTMLElement
    vi.useFakeTimers()
    box.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    vi.useRealTimers()
    const tip = document.getElementById('sq-tip')!
    expect(tip.textContent!.split('\n')).toEqual(Array.from({ length: 7 }, (_, i) => `W${i + 1} ${10 * (i + 1)}% ${i + 1}K req`))
    wrapper.unmount()
  })
})
