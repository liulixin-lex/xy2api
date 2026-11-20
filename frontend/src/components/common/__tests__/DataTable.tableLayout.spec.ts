import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { computed, defineComponent, h, nextTick, provide, ref } from 'vue'

import DataTable from '../DataTable.vue'
import { TABLE_REGION_KEY } from '@/components/layout/tableRegion'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// The table layout (mobile list plan): below 1024px the same table as on a desktop, never cards. What the cards used
// to be tested for (selection limits, off-page keys, reload in place, row-size caches, the sideways-scroll line) is
// asserted here on the table below 1024px.

/** A viewport below 768px (a phone): every min-width query fails. */
const stubPhone = () => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
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

const rows = (...ids: number[]) => ids.map((id) => ({ id, name: `row-${id}` }))
const columns = [{ key: 'name', label: 'Name', sortable: true }]

/** DataTable inside a stand-in for TablePageLayout that says whether the table region is bounded. */
const inRegion = (bounded: boolean, props: Record<string, unknown>) =>
  mount(defineComponent({
    components: { DataTable },
    setup() {
      provide(TABLE_REGION_KEY, computed(() => bounded))
      return () => h(DataTable, { layout: 'table', ...props })
    }
  }))

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('DataTable layout="table" below 1024px', () => {
  it('mounts the table, not cards, with the sticky header and no mobile sort bar or select-all line', () => {
    stubPhone()
    const wrapper = mount(DataTable, { props: { layout: 'table', columns, data: rows(1, 2), rowKey: 'id', selectable: true } })
    expect(wrapper.find('.table-cards').exists()).toBe(false)
    expect(wrapper.find('table thead.table-header').exists()).toBe(true)
    expect(wrapper.findAll('tbody tr[data-row-id]')).toHaveLength(2)
    expect(wrapper.find('[data-test="mobile-sort"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="select-all-mobile"]').exists()).toBe(false)
    expect(wrapper.get('.table-wrapper').classes()).toContain('dt-layout-table')
  })

  it('keeps the cards layout as it was for a page that has not moved', () => {
    stubPhone()
    const wrapper = mount(DataTable, { props: { columns, data: rows(1), rowKey: 'id' } })
    expect(wrapper.find('.table-cards').exists()).toBe(true)
    expect(wrapper.find('table').exists()).toBe(false)
  })

  it('freezes the built-in check box column with the first column, each check box in a label as big as its cell', () => {
    stubPhone()
    const wrapper = mount(DataTable, {
      props: { layout: 'table', columns: [...columns, { key: 'other', label: 'Other' }, { key: 'actions', label: '' }], data: rows(1), rowKey: 'id', selectable: true }
    })
    const headers = wrapper.findAll('thead th')
    expect(headers[0].classes()).toEqual(expect.arrayContaining(['sticky-col', 'sticky-col-left-first']))
    expect(headers[1].classes()).toEqual(expect.arrayContaining(['sticky-col', 'sticky-col-left-second']))
    expect(headers[2].classes()).not.toContain('sticky-col')
    expect(headers[3].classes()).toContain('sticky-col-right')
    const cells = wrapper.findAll('tbody tr[data-row-id="1"] td')
    expect(cells[0].classes()).toContain('sticky-col-left-first')
    expect(cells[1].classes()).toContain('sticky-col-left-second')
    expect(cells[0].get('label.dt-check input[data-test="select-row"]').exists()).toBe(true)
    expect(wrapper.get('th label.dt-check input[data-test="select-all"]').exists()).toBe(true)
    expect(wrapper.get('.table-wrapper').classes()).toContain('dt-builtin-select')
  })

  it('sorts from the header button: a click anywhere on the cell or the key on the focused button, aria-sort on the th', async () => {
    stubPhone()
    const wrapper = mount(DataTable, {
      props: { layout: 'table', columns, data: rows(2, 1), rowKey: 'id', serverSideSort: true }
    })
    const th = wrapper.get('thead th')
    const button = th.get('button.dt-sort[data-test="sort-name"]')
    expect(button.attributes('type')).toBe('button')
    expect(th.attributes('scope')).toBe('col')
    expect(th.attributes('aria-sort')).toBe('none')
    // Enter or Space on a button is a click on it.
    await button.trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['name', 'asc']])
    expect(th.attributes('aria-sort')).toBe('ascending')
    // The th still sorts on its own (a click on its padding), once per click.
    await th.trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['name', 'asc'], ['name', 'desc']])
    expect(th.attributes('aria-sort')).toBe('descending')
  })

  // Astra review #2: a page's own sort stays the sort when its column is hidden; only a header click stores a choice.
  it('follows the page\'s sort whatever columns are shown, and stores only a header click', async () => {
    stubPhone()
    const both = [{ key: 'id', label: 'ID', sortable: true }, { key: 'last_used_at', label: 'Last used', sortable: true }]
    localStorage.setItem('table-sort', JSON.stringify({ key: 'id', order: 'asc' }))
    const wrapper = mount(DataTable, {
      props: { layout: 'table', columns: both, data: rows(1), rowKey: 'id', serverSideSort: true, sort: { key: 'id', order: 'asc' }, sortStorageKey: 'table-sort', defaultSortKey: 'last_used_at', defaultSortOrder: 'desc' }
    })
    const ariaSort = () => Object.fromEntries(wrapper.findAll('thead th').map((th) => [th.text(), th.attributes('aria-sort')]))
    expect(ariaSort()).toEqual({ ID: 'ascending', 'Last used': 'none' })
    // The other width hides the ID column: nothing else takes the arrow, nothing is emitted or stored.
    await wrapper.setProps({ columns: [both[1]] })
    expect(ariaSort()).toEqual({ 'Last used': 'none' })
    expect(wrapper.emitted('sort')).toBeUndefined()
    expect(JSON.parse(localStorage.getItem('table-sort')!)).toEqual({ key: 'id', order: 'asc' })
    await wrapper.setProps({ columns: both })
    expect(ariaSort()).toEqual({ ID: 'ascending', 'Last used': 'none' })
    // A header click is the user's choice: emitted and stored.
    await wrapper.get('[data-test="sort-last_used_at"]').trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['last_used_at', 'asc']])
    expect(JSON.parse(localStorage.getItem('table-sort')!)).toEqual({ key: 'last_used_at', order: 'asc' })
    localStorage.clear()
  })

  // Astra review #3: keyboard focus under a frozen column. The wrapper's scroll padding is the frozen widths and the
  // header; a control the browser leaves partly covered is scrolled clear.
  it('keeps keyboard focus clear of the frozen columns and the header', async () => {
    stubPhone()
    const rect = (left: number, width: number, top = 0, height = 40) => () => ({ left, width, right: left + width, top, height, bottom: top + height, x: left, y: top, toJSON: () => ({}) }) as DOMRect
    const wrapper = mount(DataTable, {
      attachTo: document.body,
      props: { layout: 'table', columns: [{ key: 'name', label: 'Name' }, { key: 'state', label: 'State' }, { key: 'actions', label: '' }], data: rows(1), rowKey: 'id', selectable: true },
      slots: { 'cell-state': '<button data-test="switch">s</button>', 'cell-actions': '<button>⋯</button>' }
    })
    const box = wrapper.get('.table-wrapper').element as HTMLElement
    const heads = box.querySelectorAll<HTMLElement>('thead th')
    heads[0].getBoundingClientRect = rect(0, 44)
    heads[1].getBoundingClientRect = rect(44, 140)
    heads[3].getBoundingClientRect = rect(346, 44)
    box.querySelector<HTMLElement>('thead')!.getBoundingClientRect = rect(0, 390, 0, 45)
    box.getBoundingClientRect = rect(0, 390, 0, 600)
    Object.defineProperty(box, 'clientWidth', { configurable: true, get: () => 390 })
    Object.defineProperty(box, 'clientHeight', { configurable: true, get: () => 600 })
    let left = 100
    Object.defineProperty(box, 'scrollLeft', { configurable: true, get: () => left, set: (value: number) => { left = value } })
    window.dispatchEvent(new Event('resize'))
    await wrapper.setProps({ data: rows(1, 2) })
    await nextTick()
    await nextTick()
    expect(box.style.scrollPaddingLeft).toBe('184px')
    expect(box.style.scrollPaddingRight).toBe('44px')
    expect(box.style.scrollPaddingTop).toBe('45px')

    const control = box.querySelector<HTMLElement>('[data-test="switch"]')!
    control.matches = ((selector: string) => selector === ':focus-visible') as typeof control.matches
    // Under the frozen name column (184px): scrolled back by the covered part and a 4px gap.
    control.getBoundingClientRect = rect(160, 28, 100, 28)
    control.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    expect(left).toBe(100 - (184 - 160 + 4))
    // Under the frozen "⋯" (from 346px): scrolled on by the covered part and the gap.
    control.getBoundingClientRect = rect(330, 28, 100, 28)
    control.dispatchEvent(new FocusEvent('focusin', { bubbles: true }))
    expect(left).toBe(72 + (358 - 346 + 4))
    wrapper.unmount()
  })

  it('selects only eligible rows and keeps keys from other pages', async () => {
    stubPhone()
    const wrapper = mount(DataTable, {
      props: {
        layout: 'table',
        columns,
        data: [{ id: 1, name: 'Protected' }, { id: 2, name: 'Allowed' }],
        rowKey: 'id',
        selectable: true,
        selectedKeys: [99],
        rowSelectable: (row: { id: number }) => row.id === 2
      }
    })
    const header = wrapper.get<HTMLInputElement>('[data-test="select-all"]')
    const inputs = wrapper.findAll<HTMLInputElement>('[data-test="select-row"]')
    expect(inputs.map((input) => input.element.disabled)).toEqual([true, false])
    await header.setValue(true)
    expect(wrapper.emitted('update:selectedKeys')?.at(-1)?.[0]).toEqual([99, 2])
    await wrapper.setProps({ selectedKeys: [99, 2] })
    await wrapper.findAll<HTMLInputElement>('[data-test="select-row"]')[1].setValue(false)
    expect(wrapper.emitted('update:selectedKeys')?.at(-1)?.[0]).toEqual([99])
  })

  it('keeps the same row nodes through a reload with rows on screen', async () => {
    stubPhone()
    const wrapper = mount(DataTable, { props: { layout: 'table', columns, data: rows(1, 2), rowKey: 'id' } })
    const before = wrapper.get('tbody tr[data-row-id="1"]').element
    await wrapper.setProps({ loading: true })
    expect(wrapper.get('tbody tr[data-row-id="1"]').element).toBe(before)
    expect(wrapper.find('[data-test="table-progress"]').exists()).toBe(true)
    await wrapper.setProps({ data: [{ id: 1, name: 'row-1 (new)' }, { id: 2, name: 'row-2' }], loading: false })
    expect(wrapper.get('tbody tr[data-row-id="1"]').element).toBe(before)
    expect(wrapper.get('tbody tr[data-row-id="1"]').text()).toContain('row-1 (new)')
  })

  it('windows rows only inside a bounded table region; a page that scrolls renders them all', async () => {
    stubPhone()
    const data = rows(...Array.from({ length: 12 }, (_, i) => 100 + i))
    const bounded = inRegion(true, { columns, data, rowKey: 'id', virtualizeThreshold: 3 })
    await nextTick()
    const table = bounded.findComponent(DataTable).vm as unknown as { shouldVirtualize: boolean; virtualizer: any }
    expect(table.shouldVirtualize).toBe(true)
    // The size cache is keyed by row, as on a desktop.
    const instance = table.virtualizer?.value ?? table.virtualizer
    expect(instance.options.getItemKey(2)).toBe(102)

    const natural = inRegion(false, { columns, data, rowKey: 'id', virtualizeThreshold: 3 })
    await nextTick()
    expect((natural.findComponent(DataTable).vm as unknown as { shouldVirtualize: boolean }).shouldVirtualize).toBe(false)
    expect(natural.findAll('tbody tr[data-row-id]')).toHaveLength(12)
    // Outside TablePageLayout the page scrolls, no windowing: on a phone and on a desktop alike (Astra review #8).
    const alone = mount(DataTable, { props: { layout: 'table', columns, data, rowKey: 'id', virtualizeThreshold: 3 } })
    expect((alone.vm as unknown as { shouldVirtualize: boolean }).shouldVirtualize).toBe(false)
    Object.defineProperty(window, 'matchMedia', { writable: true, value: () => ({ matches: true, media: '', addEventListener: vi.fn(), removeEventListener: vi.fn() }) })
    const wide = mount(DataTable, { props: { layout: 'table', columns, data, rowKey: 'id', virtualizeThreshold: 3 } })
    expect((wide.vm as unknown as { shouldVirtualize: boolean }).shouldVirtualize).toBe(false)
    // The cards layout keeps its desktop rule.
    const legacy = mount(DataTable, { props: { columns, data, rowKey: 'id', virtualizeThreshold: 3 } })
    expect((legacy.vm as unknown as { shouldVirtualize: boolean }).shouldVirtualize).toBe(true)
  })

  it('tracks its size while mounted: the frozen columns\' edge line follows the sideways scroll', async () => {
    stubPhone()
    const observers: Array<{ callback: ResizeObserverCallback; targets: Element[] }> = []
    vi.stubGlobal('ResizeObserver', class {
      targets: Element[] = []
      constructor(public callback: ResizeObserverCallback) { observers.push(this) }
      observe(target: Element) { this.targets.push(target) }
      unobserve() {}
      disconnect() {}
    })
    const width = ref(900)
    const wrapper = mount(DataTable, { props: { layout: 'table', columns, data: rows(1), rowKey: 'id' } })
    await nextTick()
    await nextTick()
    const box = wrapper.get('.table-wrapper').element as HTMLElement
    Object.defineProperty(box, 'clientWidth', { configurable: true, get: () => 390 })
    Object.defineProperty(box, 'scrollWidth', { configurable: true, get: () => width.value })
    const observer = observers.find((item) => item.targets.includes(box))!
    observer.callback([{ target: box } as unknown as ResizeObserverEntry], observer as unknown as ResizeObserver)
    await nextTick()
    expect(box.classList.contains('is-scrollable')).toBe(true)
    width.value = 390
    observer.callback([{ target: box } as unknown as ResizeObserverEntry], observer as unknown as ResizeObserver)
    await nextTick()
    expect(box.classList.contains('is-scrollable')).toBe(false)
  })
})

// Touch sizes follow the pointer, never the width (mobile list plan, item 17): rows 48px, the header 40px, check boxes
// in 44px labels, a phone's identity column clamp(120px, 36vw, 160px) and its "⋯" column 44px; the toolbar's fields
// are 16px on touch (no zoom on focus) and its buttons 40px on any coarse pointer.
describe('touch and phone sizes', () => {
  const read = (path: string) => readFileSync(fileURLToPath(new URL(path, import.meta.url)), 'utf8')
  it('sizes the table for touch by pointer and the frozen columns for phones by width', () => {
    const table = read('../DataTable.vue')
    const coarse = table.slice(table.indexOf('@media (pointer: coarse) {\n  .dt-layout-table'))
    expect(coarse).toMatch(/\.dt-layout-table :deep\(tbody td\) \{\s*height: 48px;/)
    expect(coarse).toMatch(/\.dt-layout-table \.sticky-header-cell \{\s*height: 40px;/)
    expect(coarse).toMatch(/\.table-wrapper\.dt-builtin-select :deep\(tbody \.dt-check\) \{\s*min-height: 48px;/)
    // The built-in check box column is exactly 44px whatever padding the page gives cells (Astra review #7).
    expect(table).toMatch(/\.table-wrapper\.dt-builtin-select :deep\(\.dt-select\) \{\s*box-sizing: border-box;\s*width: 44px;\s*min-width: 44px;\s*max-width: 44px;\s*padding: 0;/)
    expect(table).toMatch(/\.table-wrapper\.dt-builtin-select \{\s*--select-col-width: 44px;/)
    const phone = table.slice(table.indexOf('@media (max-width: 767px) {\n  .dt-layout-table'))
    expect(phone).toMatch(/--table-identity-width: clamp\(120px, 36vw, 160px\);/)
    expect(phone).toMatch(/:deep\(\.sticky-col-right\) \{\s*width: 44px;/)
  })
  it('gives the toolbar 16px fields on touch and 40px buttons on any coarse pointer', () => {
    const toolbar = read('../../layout/TableToolbar.vue')
    expect(toolbar).toMatch(/@media \(pointer: coarse\) \{\s*\.table-toolbar--compact :deep\(\.table-toolbar__input\) \{\s*height: 40px;\s*font-size: 16px;/)
    // The shared Select: the sm trigger 40px on touch, the dropdown's search box 16px (no zoom on focus), sm options 40px.
    const select = read('../Select.vue')
    expect(select).toMatch(/@media \(pointer: coarse\) \{\s*\.select-trigger\.select-trigger--sm \{\s*height: 40px;/)
    const portal = select.slice(select.lastIndexOf('@media (pointer: coarse) {'))
    expect(portal).toMatch(/^@media \(pointer: coarse\) \{\s*\.select-dropdown-portal \.select-search-input \{\s*font-size: 16px;\s*\}\s*\.select-dropdown-portal--sm \.select-option \{\s*min-height: 40px;/)
    // Last in the sheet: nothing after it sets the search box's size again.
    expect(portal.slice(portal.indexOf('}\n}'))).not.toContain('.select-search-input {')
    expect(toolbar).toMatch(/@media \(any-pointer: coarse\) \{\s*\.table-toolbar--compact :deep\(\.table-toolbar__btn\) \{\s*min-height: 40px;/)
  })
})
