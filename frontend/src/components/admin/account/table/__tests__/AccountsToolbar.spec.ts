import { afterAll, beforeAll, describe, expect, it, onTestFinished, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsToolbar from '../AccountsToolbar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }) }
})
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/stores/groupCategories', () => ({ useGroupCategoriesStore: () => ({ categories: [], reload: vi.fn() }) }))

/**
 * jsdom has no layout. A tag is 90px wide; the tag box has `room` (200px), 24px less once "more filters" shows its count.
 */
const TAG = 90
let room = 200
const widthOf = (el: HTMLElement) => {
  if (el.classList.contains('tb-tag')) return el.hidden ? 0 : TAG
  if (el.classList.contains('tb-tags')) return el.closest('.tb')?.querySelector('[data-test="more-filters-count"]') ? room - 24 : room
  return 0
}
const saved = {
  offsetWidth: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth'),
  clientWidth: Object.getOwnPropertyDescriptor(Element.prototype, 'clientWidth')
}
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, get() { return widthOf(this as HTMLElement) } })
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, get() { return widthOf(this as HTMLElement) } })
})
afterAll(() => {
  if (saved.offsetWidth) Object.defineProperty(HTMLElement.prototype, 'offsetWidth', saved.offsetWidth)
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientWidth
  if (saved.clientWidth) Object.defineProperty(Element.prototype, 'clientWidth', saved.clientWidth)
})

const mountToolbar = (filters: Record<string, string>) => mount(AccountsToolbar, {
  attachTo: document.body,
  props: { filters, search: '', groups: [], liveStatus: 'live', pause: () => {}, resume: () => {} },
  global: { stubs: { LiveStatusIndicator: true } }
})
/** Opens a filter's Select (12.6) and clicks the option with this text in its panel, in the body. */
const pick = async (wrapper: ReturnType<typeof mountToolbar>, selector: string, text: string) => {
  await wrapper.get(`${selector} button`).trigger('click')
  await flushPromises()
  Array.from(document.body.querySelectorAll<HTMLElement>('.select-option')).find((option) => option.textContent?.trim() === text)!.click()
  await flushPromises()
}
const shownTags = (wrapper: ReturnType<typeof mountToolbar>) =>
  wrapper.findAll('[data-test="filter-tags"] .tb-tag').filter((tag) => !(tag.element as HTMLElement).hidden).map((tag) => tag.attributes('data-key'))

describe('AccountsToolbar', () => {
  it('measures the tags again once "more filters" shows its count, so no tag is left cut (7e-3)', async () => {
    // Platform, type and category: 90 + 96 + 96 = 282px > 200px, so category folds; the count then takes 24px and
    // type (186px) no longer fits either.
    const wrapper = mountToolbar({ platform: 'openai', type: 'apikey', group_category: '3' })
    await flushPromises()
    expect(shownTags(wrapper)).toEqual(['platform'])
    expect(wrapper.get('[data-test="more-filters-count"]').text()).toBe('2')
    wrapper.unmount()
  })

  it('folds again when the filter box narrows and once more when the width change is over (7q-7)', async () => {
    const observed: Element[] = []
    const callbacks: ResizeObserverCallback[] = []
    vi.stubGlobal('ResizeObserver', class {
      constructor(callback: ResizeObserverCallback) { callbacks.push(callback) }
      observe = (el: Element) => { observed.push(el) }
      disconnect = vi.fn()
      unobserve = vi.fn()
    })
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    onTestFinished(() => {
      vi.useRealTimers()
      vi.unstubAllGlobals()
      room = 200
    })
    const wrapper = mountToolbar({ platform: 'openai', type: 'apikey' })
    await flushPromises()
    expect(shownTags(wrapper)).toEqual(['platform', 'type'])
    // The actions widen (the live indicator's word): only the filter box, not the toolbar, changes size.
    expect(observed).toContain(wrapper.get('[data-test="toolbar-filters"]').element)
    room = 150
    callbacks.forEach((callback) => callback([], {} as ResizeObserver))
    await flushPromises()
    expect(shownTags(wrapper)).toEqual(['platform'])
    // The layout settles after the last size report: measured once more when the change is over.
    room = 90
    vi.advanceTimersByTime(200)
    await flushPromises()
    expect(shownTags(wrapper)).toEqual([])
    wrapper.unmount()
  })

  it('below 1024px shows every tag on its own row, each removable, and "more filters" counts nothing (prototype, 7l-3)', async () => {
    const matchMedia = window.matchMedia
    window.matchMedia = ((query: string) => ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} })) as unknown as typeof window.matchMedia
    const wrapper = mountToolbar({ platform: 'openai', type: 'apikey' })
    await flushPromises()
    expect(shownTags(wrapper)).toEqual(['platform', 'type'])
    expect(wrapper.find('[data-test="more-filters-count"]').exists()).toBe(false)
    await wrapper.get('[data-test="more-filters"]').trigger('click')
    expect(wrapper.findAll('[data-test="more-filters-menu"] .tb-tag')).toHaveLength(0)
    await wrapper.get('[data-test="filter-tags"] .tb-tag[data-key="type"] .tb-tag__x').trigger('click')
    expect(wrapper.emitted('change')!.at(-1)![0]).toMatchObject({ type: '' })
    wrapper.unmount()
    window.matchMedia = matchMedia
  })

  it('emits the filters with the one that changed, as the list request takes them (7d-12)', async () => {
    const wrapper = mountToolbar({ platform: 'openai', iq_status: '' })
    await flushPromises()
    await pick(wrapper, '[data-filter="quality"]', 'admin.accounts.table.quality.trial')
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: 'openai', iq_status: '', sched_quality: 'trial' }])
    // A tag's close button clears its filter only.
    await wrapper.get('[data-test="filter-tags"] .tb-tag[data-key="platform"] .tb-tag__x').trigger('click')
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: '', iq_status: '' }])
    // A filter from the menu goes by its own field name.
    await wrapper.get('[data-test="more-filters"]').trigger('click')
    await pick(wrapper, '[data-test="more-filters-menu"] [data-filter="iq"]', 'admin.accounts.iqCheckStatus.degraded')
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: 'openai', iq_status: 'degraded' }])
    await wrapper.get('[data-test="accounts-search"]').setValue('acc')
    expect(wrapper.emitted('update:search')!.at(-1)).toEqual(['acc'])
    wrapper.unmount()
  })

  it('takes a mouse pick in a Select of "more filters" and stays open, the Select closing (12.12-3)', async () => {
    const wrapper = mountToolbar({ platform: '', iq_status: '' })
    await flushPromises()
    await wrapper.get('[data-test="more-filters"]').trigger('click')
    await wrapper.get('[data-test="more-filters-menu"] [data-filter="iq"] button').trigger('click')
    await flushPromises()
    // The panel just opened is the body's last one, outside the menu: a real press, mousedown first, as a mouse sends it.
    const panel = Array.from(document.body.querySelectorAll<HTMLElement>('.select-dropdown-portal')).at(-1)!
    const option = Array.from(panel.querySelectorAll<HTMLElement>('.select-option')).find((el) => el.textContent?.trim() === 'admin.accounts.iqCheckStatus.degraded')!
    for (const type of ['mousedown', 'mouseup', 'click']) {
      option.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true }))
      await flushPromises()
    }
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: '', iq_status: 'degraded' }])
    expect(wrapper.find('[data-test="more-filters-menu"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="more-filters-menu"] [data-filter="iq"] button').attributes('aria-expanded')).toBe('false')
    // A press really outside closes the menu.
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    await flushPromises()
    expect(wrapper.find('[data-test="more-filters-menu"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('folds the direct filters, now Selects, into "more filters" from the right while the row overflows (12.6)', async () => {
    // A filter is 100px; the filter box ends at 250px: platform and status stay (208px with the 8px gap).
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const right = this.classList.contains('tb-filters') ? this.querySelectorAll('.tb-sel').length * 100 : this.dataset.test === 'toolbar-filters' ? 250 : 0
      return { left: 0, top: 0, bottom: 0, height: 0, width: right, right, x: 0, y: 0, toJSON: () => ({}) } as DOMRect
    })
    onTestFinished(() => { vi.restoreAllMocks() })
    const wrapper = mountToolbar({})
    await flushPromises()
    const keys = (selector: string) => wrapper.findAll(`${selector} [data-filter]`).map((filter) => filter.attributes('data-filter'))
    expect(keys('.tb-filters')).toEqual(['platform', 'status'])
    expect(wrapper.get('[data-filter="platform"] button').text()).toBe('admin.accounts.table.filterLabels.platform')
    await wrapper.get('[data-test="more-filters"]').trigger('click')
    expect(keys('[data-test="more-filters-menu"]')).toEqual(['quality', 'group', 'type', 'privacy', 'iq'])
    wrapper.unmount()
  })

  it('opens "more filters" under its button, not at the toolbar\'s left edge (7e-5)', async () => {
    const wrapper = mountToolbar({})
    await flushPromises()
    const rect = (left: number, width: number) => () => ({ left, width, right: left + width, top: 0, bottom: 36, height: 36, x: left, y: 0, toJSON: () => ({}) })
    wrapper.element.getBoundingClientRect = rect(100, 1200)
    const button = wrapper.get('[data-test="more-filters"]')
    button.element.getBoundingClientRect = rect(640, 100)
    await button.trigger('click')
    await flushPromises()
    expect((wrapper.get('[data-test="more-filters-menu"]').element as HTMLElement).style.left).toBe('540px')
    wrapper.unmount()
  })
})
