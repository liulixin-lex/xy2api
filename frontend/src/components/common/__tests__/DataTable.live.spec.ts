import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive } from 'vue'

import DataTable from '../DataTable.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const stubViewport = (desktop: boolean) => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: desktop,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn()
    }))
  })
}

interface Row {
  id: number
  name: string
  dim?: boolean
}
const columns = [{ key: 'name', label: 'Name', sortable: true }]
const rows = (...ids: number[]): Row[] => ids.map((id) => ({ id, name: `row-${id}` }))

/** The element's scrollTop as a plain value (jsdom does no layout). */
const trackScrollTop = (el: Element, start: number) => {
  let top = start
  Object.defineProperty(el, 'scrollTop', { configurable: true, get: () => top, set: (value: number) => { top = value } })
  return () => top
}
const placeAt = (el: Element, top: number, height = 0) => {
  el.getBoundingClientRect = () => ({ x: 0, y: top, top, left: 0, right: 0, bottom: top + height, width: 0, height, toJSON: () => ({}) })
}
const scrollable = (el: HTMLElement, scrollHeight: number, clientHeight: number) => {
  Object.defineProperty(el, 'scrollHeight', { configurable: true, value: scrollHeight })
  Object.defineProperty(el, 'clientHeight', { configurable: true, value: clientHeight })
}
/** A container in the document for the table, with inline styles (jsdom reads them through getComputedStyle). */
const host = (style: string) => {
  const el = document.createElement('div')
  el.setAttribute('style', style)
  document.body.appendChild(el)
  return el
}
const progressShift = (wrapper: { find: (selector: string) => { attributes: (name: string) => string | undefined } }) =>
  wrapper.find('[data-test="table-progress"]').attributes('style') ?? ''

afterEach(() => {
  vi.restoreAllMocks()
})

describe('DataTable row classes', () => {
  it('applies rowClass to desktop rows, re-rendering only the row whose classes change', async () => {
    stubViewport(true)
    const data = reactive(rows(1, 2))
    const calls: number[] = []
    const rowClass = (row: Row) => {
      calls.push(row.id)
      return { 'opacity-50': row.dim }
    }
    const wrapper = mount(DataTable, { props: { columns, data, rowKey: 'id', rowClass } })
    await nextTick()
    expect(wrapper.get('tr[data-row-id="2"]').classes()).not.toContain('opacity-50')

    calls.length = 0
    data[1].dim = true
    await nextTick()
    expect(wrapper.get('tr[data-row-id="2"]').classes()).toContain('opacity-50')
    expect(wrapper.get('tr[data-row-id="1"]').classes()).not.toContain('opacity-50')
    expect(calls).toEqual([2])
  })

  it('applies rowClass to mobile cards', () => {
    stubViewport(false)
    const wrapper = mount(DataTable, { props: { columns, data: [{ ...rows(1)[0], dim: true }], rowKey: 'id', rowClass: (row: Row) => row.dim && 'opacity-50' } })
    expect(wrapper.get('[data-field="name"]').element.closest('.rounded-lg')!.classList).toContain('opacity-50')
  })
})

describe('DataTable scroll to top', () => {
  it('scrolls the desktop table up on its own sort and on a batch with no row in common, not on reloads of the same rows', async () => {
    stubViewport(true)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2, 3), rowKey: 'id', serverSideSort: true } })
    await nextTick()
    const top = trackScrollTop(wrapper.get('.table-wrapper').element, 300)

    await wrapper.setProps({ data: rows(2, 3, 4) }) // a reload after a row action
    await wrapper.setProps({ data: rows(2, 3, 4) }) // a passive update: new objects, same rows
    expect(top()).toBe(300)

    await wrapper.setProps({ data: rows(7, 8, 9) }) // another page
    expect(top()).toBe(0)

    trackScrollTop(wrapper.get('.table-wrapper').element, 300)
    await wrapper.get('th').trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['name', 'asc']])
    expect(wrapper.get('.table-wrapper').element.scrollTop).toBe(0)
  })

  it('also resets the virtualizer when the table is virtualized', async () => {
    stubViewport(true)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2, 3), rowKey: 'id', virtualizeThreshold: 1 } })
    await nextTick()
    const vm = wrapper.vm as unknown as { shouldVirtualize: boolean; virtualizer: { scrollToOffset: (offset: number) => void }; scrollToTop: () => void }
    expect(vm.shouldVirtualize).toBe(true)
    const scrollToOffset = vi.spyOn(vm.virtualizer, 'scrollToOffset')
    vm.scrollToTop()
    expect(scrollToOffset).toHaveBeenCalledWith(0)
  })

  it('brings the mobile list back into view when its top scrolled above the window, and leaves it alone otherwise', async () => {
    stubViewport(false)
    const scrollBy = vi.spyOn(window, 'scrollBy').mockImplementation(() => undefined)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2), rowKey: 'id' }, attachTo: document.body })
    placeAt(wrapper.element, -400)

    await wrapper.setProps({ data: rows(1, 3) })
    expect(scrollBy).not.toHaveBeenCalled()
    await wrapper.setProps({ data: rows(5, 6) })
    expect(scrollBy).toHaveBeenCalledWith(0, -400)

    scrollBy.mockClear()
    placeAt(wrapper.element, 120) // in view
    ;(wrapper.vm as unknown as { scrollToTop: () => void }).scrollToTop()
    expect(scrollBy).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('DataTable scroll to top, more cases', () => {
  it('scrolls up when a page sort is picked in the mobile sort bar', async () => {
    stubViewport(false)
    const scrollBy = vi.spyOn(window, 'scrollBy').mockImplementation(() => undefined)
    const wrapper = mount(DataTable, {
      props: { columns, data: rows(1, 2), rowKey: 'id', pageSorts: [{ key: 'usage', label: 'Usage' }] },
      attachTo: document.body
    })
    placeAt(wrapper.element, -400)
    await wrapper.get('[data-test="mobile-sort-key"]').setValue('usage')
    expect(wrapper.emitted('pageSort')).toEqual([['usage']])
    expect(scrollBy).toHaveBeenCalledWith(0, -400)
    wrapper.unmount()
  })

  it('does not scroll on its own for rows without a stable key', async () => {
    stubViewport(true)
    const unkeyed = (...names: string[]) => names.map((name) => ({ name }))
    const wrapper = mount(DataTable, { props: { columns, data: unkeyed('a', 'b') } })
    await nextTick()
    const top = trackScrollTop(wrapper.get('.table-wrapper').element, 300)
    await wrapper.setProps({ data: unkeyed('c', 'd') }) // new objects, nothing to compare them by
    expect(top()).toBe(300)
  })
})

describe('DataTable progress bar in view', () => {
  it('rides down under the page header when the page scrolls, past a card that clips but does not scroll', async () => {
    stubViewport(false)
    const card = host('overflow: hidden')
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2), rowKey: 'id' }, attachTo: card })
    const list = wrapper.element as HTMLElement
    list.style.scrollMarginTop = '64px'
    placeAt(list, -300, 2000)
    await wrapper.setProps({ loading: true })
    expect(progressShift(wrapper)).toContain('translateY(364px)')

    placeAt(list, 200, 2000) // scrolled back: the list top is in view
    window.dispatchEvent(new Event('scroll'))
    await nextTick()
    expect(progressShift(wrapper)).not.toContain('translate')
    wrapper.unmount()
    card.remove()
  })

  it('stops at the top of a scrolling dialog body, with no page header there', async () => {
    stubViewport(false)
    const body = host('overflow-y: auto')
    scrollable(body, 1000, 400)
    placeAt(body, 100)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2), rowKey: 'id' }, attachTo: body })
    const list = wrapper.element as HTMLElement
    list.style.scrollMarginTop = '64px'
    placeAt(list, 40, 800)
    await wrapper.setProps({ loading: true })
    expect(progressShift(wrapper)).toContain('translateY(60px)')
    wrapper.unmount()
    body.remove()
  })

  it('follows the window on the desktop table when its wrapper does not scroll itself (tablet widths, long pages)', async () => {
    stubViewport(true)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2), rowKey: 'id' }, attachTo: document.body })
    await nextTick()
    const table = wrapper.get('.table-wrapper').element as HTMLElement
    table.style.scrollMarginTop = '64px'
    placeAt(table, -200, 1500)
    await wrapper.setProps({ loading: true })
    expect(progressShift(wrapper)).toContain('translateY(264px)')

    await wrapper.setProps({ loading: false })
    scrollable(table, 3000, 600) // the fixed-height layout: the wrapper scrolls, sticky keeps the bar at its top
    await wrapper.setProps({ loading: true })
    expect(progressShift(wrapper)).not.toContain('translate')
    wrapper.unmount()
  })
})

describe('DataTable mobile progress bar', () => {
  it('comes first in the list and leaves the list classes unchanged', async () => {
    stubViewport(false)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2), rowKey: 'id' } })
    const classes = wrapper.classes()
    await wrapper.setProps({ loading: true })
    expect(wrapper.element.firstElementChild!.getAttribute('data-test')).toBe('table-progress')
    expect(wrapper.classes()).toEqual(classes)
  })
})
