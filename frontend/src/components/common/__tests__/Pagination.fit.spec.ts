import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import Pagination from '../Pagination.vue'

// The wide row shows as many pages around the current one as it holds (Astra post-acceptance review, suggestion 3):
// 1024px with thousands of pages ran past the row's edge. jsdom lays nothing out, so the row's widths are stubbed:
// each button 50px, the row 400px.

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

const BUTTON = 50
const ROW = 400
function mountWide(props: Record<string, unknown>, rowWidth = ROW) {
  const wrapper = mount(Pagination, {
    props: { pageSize: 20, showPageSizeSelector: true, ...props },
    global: { stubs: { Icon: true, Select: { props: ['ariaLabel'], template: '<button class="select-trigger" :aria-label="ariaLabel" />' } } },
    attachTo: document.body
  })
  const row = wrapper.get('.pager-wide').element as HTMLElement
  Object.defineProperty(row, 'clientWidth', { configurable: true, get: () => rowWidth })
  Object.defineProperty(row, 'scrollWidth', { configurable: true, get: () => row.querySelectorAll('nav button').length * BUTTON })
  return wrapper
}
const pages = (wrapper: ReturnType<typeof mountWide>) =>
  wrapper.findAll('nav[aria-label="Pagination"] button').map((button) => button.text()).filter(Boolean)

describe('Pagination fits its pages to the wide row', () => {
  it('drops pages either side of the current one until the row holds them', async () => {
    const wrapper = mountWide({ total: 200_000, page: 4999 })
    await wrapper.setProps({ page: 5000 })
    await flushPromises()
    // 2 either side would be 11 buttons (550px), 1 would be 9 (450px): 0 leaves 7 (350px).
    expect(pages(wrapper)).toEqual(['1', '...', '5000', '...', '10000'])
  })

  it('keeps two either side where they fit, and refits when the page changes', async () => {
    const wrapper = mountWide({ total: 100, page: 1 })
    await wrapper.setProps({ page: 2 })
    await flushPromises()
    expect(pages(wrapper)).toEqual(['1', '2', '3', '4', '5'])
    // Page 3 of 10,000: 2 either side would be 9 buttons (450px), 1 leaves 8 (400px).
    await wrapper.setProps({ total: 200_000, page: 3 })
    await flushPromises()
    expect(pages(wrapper)).toEqual(['1', '2', '3', '4', '...', '10000'])
  })

  it('gives the pages a line of their own only when none either side still does not fit', async () => {
    const wide = mountWide({ total: 200_000, page: 4999 })
    await wide.setProps({ page: 5000 })
    await flushPromises()
    expect(wide.get('.pager-wide').classes()).not.toContain('pager-wide--wrap')

    const narrow = mountWide({ total: 200_000, page: 4999 }, 300)
    await narrow.setProps({ page: 5000 })
    await flushPromises()
    expect(pages(narrow)).toEqual(['1', '...', '5000', '...', '10000'])
    expect(narrow.get('.pager-wide').classes()).toContain('pager-wide--wrap')
  })
})
