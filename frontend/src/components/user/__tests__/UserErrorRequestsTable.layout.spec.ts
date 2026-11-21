// Mobile list plan (step 1): the user usage page's errors tab, a table at every width, its columns and row action as
// they were.
import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UserErrorRequestsTable from '../UserErrorRequestsTable.vue'

const { getDetail } = vi.hoisted(() => ({ getDetail: vi.fn() }))
vi.mock('@/api/usage', async () => ({ ...(await vi.importActual<typeof import('@/api/usage')>('@/api/usage')), getMyErrorDetail: getDetail }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

describe('UserErrorRequestsTable layout', () => {
  it('keeps its rows opening their detail', () => {
    const DataTableStub = { name: 'DataTable', props: { clickableRows: Boolean }, template: '<div />' }
    const wrapper = mount(UserErrorRequestsTable, {
      props: { rows: [], total: 0, loading: false, page: 1, pageSize: 20 },
      global: { stubs: { DataTable: DataTableStub, Pagination: true, UserErrorDetailModal: true } },
    })
    const table = wrapper.findComponent(DataTableStub)
    expect(table.props('clickableRows')).toBe(true)
  })

  // Re-review item 1: the key name is the frozen first column on tablets and phones.
  it('caps the key name at 240px, cut with its whole text in the tip', () => {
    const name = 'production-key-for-the-billing-and-analytics-pipeline-with-a-very-long-name-xyz'
    const wrapper = mount(UserErrorRequestsTable, {
      props: { rows: [{ id: 1, created_at: '2026-06-05T23:59:50Z', key_name: name, key_deleted: true } as never], total: 1, loading: false, page: 1, pageSize: 20 },
      global: { stubs: { Pagination: true, UserErrorDetailModal: true, Teleport: true } },
    })
    const cell = wrapper.get('[data-testid="error-key-cell"]')
    expect(cell.classes()).toContain('max-lg:max-w-[240px]')
    const text = cell.get('span')
    expect(text.classes()).toContain('truncate')
    expect(text.attributes('title')).toBe(name)
    expect(cell.findAll('span')[1].classes()).toContain('text-xs')
  })

  // Second re-review: on a touch screen the cut name has no tip; a tap on the row opens the detail with the whole name.
  it('opens the row\'s detail on a click, the whole key name in it', async () => {
    const name = 'production-key-for-the-billing-and-analytics-pipeline-with-a-very-long-name-xyz'
    const row = { id: 7, created_at: '2026-06-05T23:59:50Z', key_name: name, key_deleted: false, model: 'gpt-6.1-sol', status_code: 502, category: 'upstream' }
    getDetail.mockResolvedValueOnce(row)
    const wrapper = mount(UserErrorRequestsTable, {
      props: { rows: [row as never], total: 1, loading: false, page: 1, pageSize: 20 },
      global: { stubs: { Pagination: true, Teleport: true } },
      attachTo: document.body,
    })
    expect(wrapper.find('[data-testid="error-detail-key"]').exists()).toBe(false)
    await wrapper.get('tbody tr').trigger('click')
    await flushPromises()
    expect(getDetail).toHaveBeenCalledWith(7)
    expect(document.querySelector('[data-testid="error-detail-key"] p')?.textContent?.trim()).toBe(name)
    wrapper.unmount()
  })

  // Browser acceptance (step 1), items 5 and 6: a details column frozen on the right, whatever columns are chosen; its
  // header an icon named for screen readers, its 40px button opening the row's detail.
  it('adds a 44px details column after the chosen ones, its button opening the detail', async () => {
    getDetail.mockResolvedValueOnce({ id: 3, created_at: '2026-06-05T23:59:50Z', key_name: 'k', key_deleted: false, status_code: 500, category: 'upstream' })
    const wrapper = mount(UserErrorRequestsTable, {
      props: { rows: [{ id: 3, created_at: '2026-06-05T23:59:50Z', key_name: 'k', key_deleted: false } as never], total: 1, loading: false, page: 1, pageSize: 20, visibleColumnKeys: ['key_name', 'status'] },
      global: { stubs: { Pagination: true, Teleport: true, Icon: true } },
      attachTo: document.body,
    })
    const ths = wrapper.findAll('th')
    expect(ths).toHaveLength(3)
    expect(ths[2].classes()).toEqual(expect.arrayContaining(['w-11', 'min-w-11', '!px-0']))
    const header = ths[2].get('[data-testid="error-detail-header"]')
    expect(header.attributes('role')).toBe('img')
    expect(header.attributes('aria-label')).toBe('usage.errors.detail.button')
    const button = wrapper.get('[data-testid="error-detail-open"]')
    expect(button.classes()).toEqual(expect.arrayContaining(['h-10', 'w-10']))
    expect(button.attributes('aria-label')).toBe('usage.errors.detail.open')
    await button.trigger('click')
    await flushPromises()
    expect(getDetail).toHaveBeenLastCalledWith(3)
    wrapper.unmount()
  })
})
