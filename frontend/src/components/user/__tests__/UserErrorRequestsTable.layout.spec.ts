// Mobile list plan (step 1): the user usage page's errors tab uses the table at every width, its columns and row action
// as they were.
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UserErrorRequestsTable from '../UserErrorRequestsTable.vue'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

describe('UserErrorRequestsTable layout', () => {
  it('asks DataTable for the table, rows still opening their detail', () => {
    const DataTableStub = { name: 'DataTable', props: { layout: String, clickableRows: Boolean }, template: '<div />' }
    const wrapper = mount(UserErrorRequestsTable, {
      props: { rows: [], total: 0, loading: false, page: 1, pageSize: 20 },
      global: { stubs: { DataTable: DataTableStub, Pagination: true, UserErrorDetailModal: true } },
    })
    const table = wrapper.findComponent(DataTableStub)
    expect(table.props('layout')).toBe('table')
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
    expect(cell.classes()).toContain('max-w-[240px]')
    const text = cell.get('span')
    expect(text.classes()).toContain('truncate')
    expect(text.attributes('title')).toBe(name)
    expect(cell.findAll('span')[1].classes()).toContain('text-xs')
  })
})
