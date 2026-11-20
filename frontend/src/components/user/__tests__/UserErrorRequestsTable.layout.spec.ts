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
})
