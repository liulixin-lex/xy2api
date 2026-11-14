import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

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

const columns = [{ key: 'name', label: 'Name' }]
const page = (suffix = '') => [
  { id: 1, name: `One${suffix}` },
  { id: 2, name: `Two${suffix}` }
]

describe('DataTable reload with rows on screen', () => {
  it('keeps the same row nodes on the desktop table, shows a progress bar and aria-busy, and no skeleton', async () => {
    stubViewport(true)
    const wrapper = mount(DataTable, { props: { columns, data: page(), rowKey: 'id' } })
    await wrapper.vm.$nextTick()
    const before = wrapper.get('tbody tr[data-row-id="1"]').element

    await wrapper.setProps({ loading: true })
    expect(wrapper.get('tbody tr[data-row-id="1"]').element).toBe(before)
    expect(wrapper.find('tbody .animate-pulse').exists()).toBe(false)
    expect(wrapper.find('[data-test="table-progress"]').exists()).toBe(true)
    expect(wrapper.get('.table-wrapper').attributes('aria-busy')).toBe('true')
    expect(wrapper.get('.table-wrapper').attributes('inert')).toBeUndefined()

    // The reload answers with new objects for the same rows: the nodes are patched in place.
    await wrapper.setProps({ data: page(' (new)'), loading: false })
    const after = wrapper.get('tbody tr[data-row-id="1"]')
    expect(after.element).toBe(before)
    expect(after.text()).toContain('One (new)')
    expect(wrapper.find('[data-test="table-progress"]').exists()).toBe(false)
    expect(wrapper.get('.table-wrapper').attributes('aria-busy')).toBeUndefined()
  })

  it('keeps the same cards on the mobile layout while reloading', async () => {
    stubViewport(false)
    const wrapper = mount(DataTable, { props: { columns, data: page(), rowKey: 'id' } })
    const card = () => wrapper.get('[data-field="name"]').element.closest('.table-card')
    const before = card()

    await wrapper.setProps({ loading: true })
    expect(card()).toBe(before)
    expect(wrapper.find('.animate-pulse').exists()).toBe(false)
    expect(wrapper.find('[data-test="table-progress"]').exists()).toBe(true)
    expect(wrapper.attributes('aria-busy')).toBe('true')

    await wrapper.setProps({ data: page(' (new)'), loading: false })
    expect(card()).toBe(before)
    expect(wrapper.get('[data-field="name"]').text()).toContain('One (new)')
  })

  it('still shows the skeleton for a load with no rows yet', async () => {
    stubViewport(true)
    const wrapper = mount(DataTable, { props: { columns, data: [], loading: true } })
    expect(wrapper.findAll('tbody tr .animate-pulse')).toHaveLength(5)
    expect(wrapper.find('[data-test="table-progress"]').exists()).toBe(false)
  })
})
