import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsToolbar from '../AccountsToolbar.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }) }
})
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/stores/groupCategories', () => ({ useGroupCategoriesStore: () => ({ categories: [], reload: vi.fn() }) }))

/**
 * jsdom has no layout. A tag is 90px wide; the tag box has 200px, 24px less once "more filters" shows its count.
 */
const TAG = 90
const widthOf = (el: HTMLElement) => {
  if (el.classList.contains('tb-tag')) return el.hidden ? 0 : TAG
  if (el.classList.contains('tb-tags')) return el.closest('.tb')?.querySelector('[data-test="more-filters-count"]') ? 176 : 200
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

  it('emits the filters with the one that changed, as the list request takes them (7d-12)', async () => {
    const wrapper = mountToolbar({ platform: 'openai', iq_status: '' })
    await flushPromises()
    await wrapper.get('select[data-filter="quality"]').setValue('substandard')
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: 'openai', iq_status: '', sched_quality: 'substandard' }])
    // A tag's close button clears its filter only.
    await wrapper.get('[data-test="filter-tags"] .tb-tag[data-key="platform"] .tb-tag__x').trigger('click')
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: '', iq_status: '' }])
    // A filter from the menu goes by its own field name.
    await wrapper.get('[data-test="more-filters"]').trigger('click')
    await wrapper.get('[data-test="more-filters-menu"] select[data-filter="iq"]').setValue('degraded')
    expect(wrapper.emitted('change')!.at(-1)).toEqual([{ platform: 'openai', iq_status: 'degraded' }])
    await wrapper.get('[data-test="accounts-search"]').setValue('acc')
    expect(wrapper.emitted('update:search')!.at(-1)).toEqual(['acc'])
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
