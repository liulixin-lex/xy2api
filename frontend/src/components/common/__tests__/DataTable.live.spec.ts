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
const placeAt = (el: Element, top: number) => {
  el.getBoundingClientRect = () => ({ x: 0, y: top, top, left: 0, right: 0, bottom: top, width: 0, height: 0, toJSON: () => ({}) })
}

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

describe('DataTable mobile progress bar', () => {
  it('comes and goes without changing the list classes or what comes before the first card', async () => {
    stubViewport(false)
    const wrapper = mount(DataTable, { props: { columns, data: rows(1, 2), rowKey: 'id' } })
    const firstCard = wrapper.get('[data-field="name"]').element.closest('.rounded-lg')!
    const before = firstCard.previousElementSibling
    const classes = wrapper.classes()
    await wrapper.setProps({ loading: true })
    expect(wrapper.find('[data-test="table-progress"]').exists()).toBe(true)
    expect(firstCard.previousElementSibling).toBe(before) // the stack's spacing before the first card stays
    expect(wrapper.classes()).toEqual(classes)
  })
})

describe('DataTable sideways scroll state', () => {
  it('drops is-scrollable when the table narrows after the wrapper resized (7e-7)', async () => {
    stubViewport(true)
    const observers: Array<{ callback: () => void; targets: Element[] }> = []
    vi.stubGlobal('ResizeObserver', class {
      targets: Element[] = []
      constructor(public callback: () => void) { observers.push(this) }
      observe(target: Element) { this.targets.push(target) }
      unobserve() {}
      disconnect() {}
    })
    const wrapper = mount(DataTable, { props: { columns, data: rows(1), rowKey: 'id' } })
    await nextTick()
    const box = wrapper.get('.table-wrapper').element as HTMLElement
    const table = box.querySelector(':scope > table')!
    let tableWidth = 1500
    Object.defineProperty(box, 'clientWidth', { configurable: true, get: () => 1300 })
    Object.defineProperty(box, 'scrollWidth', { configurable: true, get: () => Math.max(1300, tableWidth) })
    const observer = observers.find((item) => item.targets.includes(box))!
    // The wrapper narrowed first: the table still has its old width, so it scrolls.
    observer.callback()
    await nextTick()
    expect(box.classList.contains('is-scrollable')).toBe(true)
    // Then the page narrows its columns: only the table changes size, and it is watched too.
    expect(observer.targets).toContain(table)
    tableWidth = 1290
    observer.callback()
    await nextTick()
    expect(box.classList.contains('is-scrollable')).toBe(false)
    wrapper.unmount()
    vi.unstubAllGlobals()
  })
})
