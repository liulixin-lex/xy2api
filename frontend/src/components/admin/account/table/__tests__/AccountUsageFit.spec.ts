import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { h, nextTick, ref } from 'vue'

import AccountUsageFit from '../AccountUsageFit.vue'
import UsageProgressBar from '@/components/account/UsageProgressBar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }) }
})

const count = ref(0)
const mountWith = (n: number) => {
  count.value = n
  return mount(AccountUsageFit, {
    attachTo: document.body,
    slots: { default: () => Array.from({ length: count.value }, (_, i) => h(UsageProgressBar, { key: i, label: `W${i + 1}`, utilization: 10 * (i + 1), color: 'indigo' })) }
  })
}
/** The labels of the windows shown. */
const shown = (wrapper: ReturnType<typeof mountWith>) =>
  wrapper.findAll('[data-usage-window]:not([data-usage-folded])').map((el) => el.findAll('span')[0].text())

// The usage windows column keeps to the 78px row by count (7e-6, 7g-3): up to four all show, else three and "+N".
describe('AccountUsageFit', () => {
  it('shows up to four windows in full, with no "+N"', async () => {
    const wrapper = mountWith(4)
    await nextTick()
    expect(shown(wrapper)).toHaveLength(4)
    expect(wrapper.find('[data-test="usage-more"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the first three of more than four, counts the rest in "+N", lists every window in the tip, and follows the count', async () => {
    const wrapper = mountWith(7)
    await nextTick()
    expect(shown(wrapper)).toEqual(['W1', 'W2', 'W3'])
    expect(wrapper.get('[data-test="usage-more"]').text()).toBe('admin.accounts.table.usageMore:{"n":4}')

    const box = wrapper.get('[data-test="usage-fit"]').element as HTMLElement
    vi.useFakeTimers()
    box.dispatchEvent(new Event('mouseenter'))
    vi.advanceTimersByTime(400)
    vi.useRealTimers()
    // Each window's texts apart, not run together into "W110%" (7i-7).
    expect(document.getElementById('sq-tip')!.textContent!.split('\n')).toEqual(Array.from({ length: 7 }, (_, i) => `W${i + 1} · ${10 * (i + 1)}%`))

    count.value = 4
    await nextTick()
    await nextTick()
    expect(shown(wrapper)).toHaveLength(4)
    expect(wrapper.find('[data-test="usage-more"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // Astra review #9: a single window's chips are in the tip too, and touch has no hover. A tap shows the tip at once
  // (no hover delay), keyboard focus too; a tap on a control inside does that control's job only.
  it('shows the details at once on a tap and on keyboard focus, with any number of windows', async () => {
    const wrapper = mountWith(1)
    await nextTick()
    const box = wrapper.get('[data-test="usage-fit"]')
    expect(box.attributes('tabindex')).toBe('0')
    await box.trigger('click')
    expect(document.getElementById('sq-tip')!.hidden).toBe(false)
    expect(document.getElementById('sq-tip')!.textContent).toBe('W1 · 10%')
    wrapper.unmount()
    expect(mountWith(0).get('[data-test="usage-fit"]').attributes('tabindex')).toBeUndefined()
  })

  it('writes the windows at 12px in the table, each on one line', () => {
    const file = '../AccountUsageFit.vue'
    const source = readFileSync(fileURLToPath(new URL(file, import.meta.url)), 'utf8')
    expect(source).toMatch(/\.usage-fit__inner :deep\(\[class\*='text-\[9px\]'\]\),\s*\.usage-fit__inner :deep\(\[class\*='text-\[10px\]'\]\),\s*\.usage-fit__inner :deep\(\[class\*='text-\[11px\]'\]\) \{\s*font-size: var\(--font-size-small\);/)
    expect(source).toMatch(/:deep\(\[data-usage-window\] \.w-\\\[32px\\\]\) \{\s*width: auto;\s*min-width: 32px;\s*white-space: nowrap;/)
  })
})
