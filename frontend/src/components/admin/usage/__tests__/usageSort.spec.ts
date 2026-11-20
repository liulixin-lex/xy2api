// Review item 7 (mobile list plan, step 1): the usage lists and the errors tabs sort on the server, and their headers
// follow the page's own sort. Sort by model, hide the model column (phone columns or the settings): no other header
// claims the sort (the request keeps model); show it again: its arrow is back. Real DataTable, no stub.
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, h, reactive } from 'vue'

import UsageTable from '../UsageTable.vue'
import OpsErrorLogTable from '@/views/admin/ops/components/OpsErrorLogTable.vue'
import UserErrorRequestsTable from '@/components/user/UserErrorRequestsTable.vue'

vi.mock('@/utils/ipGeoLookup', () => ({ getEntry: vi.fn(() => ({ status: 'idle' })), fetchOne: vi.fn(), fetchBatch: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

const ariaSort = (wrapper: ReturnType<typeof mount>, label: string) =>
  wrapper.findAll('th').find((th) => th.text().includes(label))?.attributes('aria-sort')

describe('usage list headers follow the page sort', () => {
  const model = { key: 'model', label: 'Model', sortable: true }
  const time = { key: 'created_at', label: 'Time', sortable: true }

  it('the usage table: hidden sorted column, no false arrow; shown again, its arrow', async () => {
    const wrapper = mount(UsageTable, {
      props: { data: [], columns: [time], serverSideSort: true, sort: { key: 'model', order: 'asc' } },
      global: { stubs: { Icon: true, Teleport: true, EmptyState: true } },
    })
    expect(ariaSort(wrapper, 'Time')).toBe('none')
    await wrapper.setProps({ columns: [model, time] })
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, 'Model')).toBe('ascending')
    expect(ariaSort(wrapper, 'Time')).toBe('none')
  })

  it.each([
    ['admin errors', OpsErrorLogTable, ['model', 'status', 'created_at'], 'admin.ops.errorLog.model', 'admin.ops.errorLog.time'],
    ['user errors', UserErrorRequestsTable, ['model', 'status', 'created_at'], 'usage.errors.model', 'usage.errors.time'],
  ] as const)('the %s table: sort by model, hide it, show it again', async (_, Table, keys, modelLabel, timeLabel) => {
    const wrapper = mount(Table as never, {
      props: { rows: [], total: 0, loading: false, page: 1, pageSize: 20, sortBy: 'model', sortOrder: 'asc', visibleColumnKeys: keys.filter((key) => key !== 'model') },
      global: { stubs: { Icon: true, Teleport: true, Pagination: true, 'el-tooltip': true, UserErrorDetailModal: true } },
    })
    expect(ariaSort(wrapper, timeLabel)).toBe('none')
    await wrapper.setProps({ visibleColumnKeys: [...keys] } as never)
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, modelLabel)).toBe('ascending')
    expect(ariaSort(wrapper, timeLabel)).toBe('none')
  })

  it('names each table\'s scroll area', () => {
    const usage = mount(UsageTable, {
      props: { data: [], columns: [time], scrollLabel: 'usage.tabs.usage' },
      global: { stubs: { Icon: true, Teleport: true, EmptyState: true } },
    })
    expect(usage.find('[role="region"][aria-label="usage.tabs.usage"]').exists()).toBe(true)
    for (const Table of [OpsErrorLogTable, UserErrorRequestsTable]) {
      const errors = mount(Table as never, {
        props: { rows: [], total: 0, loading: false, page: 1, pageSize: 20, scrollLabel: 'usage.tabs.errors' },
        global: { stubs: { Icon: true, Teleport: true, Pagination: true, 'el-tooltip': true, UserErrorDetailModal: true } },
      })
      expect(errors.find('[role="region"][aria-label="usage.tabs.errors"]').exists()).toBe(true)
    }
  })

  it('maps the errors API key status_code back to the status header', async () => {
    const wrapper = mount(OpsErrorLogTable, {
      props: { rows: [], total: 0, loading: false, page: 1, pageSize: 20, sortBy: 'status_code', sortOrder: 'desc' },
      global: { stubs: { Icon: true, Teleport: true, Pagination: true, 'el-tooltip': true } },
    })
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, 'admin.ops.errorLog.status')).toBe('descending')
    expect(ariaSort(wrapper, 'admin.ops.errorLog.time')).toBe('none')
  })
})

// The shared layer's page-owned sort (872f669bb): a header click only asks; the arrows follow the sort the page passes
// back. Each page answers in its @sort, so a click shows at once, and survives hiding and showing the column.
describe('a header click asks the page, whose answer the headers show', () => {
  const stubs = { Icon: true, Teleport: true, EmptyState: true, Pagination: true, 'el-tooltip': true, UserErrorDetailModal: true }
  const model = { key: 'model', label: 'Model', sortable: true }
  const time = { key: 'created_at', label: 'Time', sortable: true }

  it('the usage table', async () => {
    const page = reactive({ sort: { key: 'created_at', order: 'desc' as 'asc' | 'desc' }, columns: [model, time], answers: true })
    const Host = defineComponent(() => () => h(UsageTable, {
      data: [], columns: page.columns, serverSideSort: true, sort: page.sort,
      onSort: (key: string, order: 'asc' | 'desc') => { if (page.answers) page.sort = { key, order } },
    }))
    const wrapper = mount(Host, { global: { stubs } })
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, 'Time')).toBe('descending')
    await wrapper.get('[data-test="sort-model"]').trigger('click')
    expect(page.sort).toEqual({ key: 'model', order: 'asc' })
    expect(ariaSort(wrapper, 'Model')).toBe('ascending')
    expect(ariaSort(wrapper, 'Time')).toBe('none')
    page.columns = [time]
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, 'Time')).toBe('none')
    page.columns = [model, time]
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, 'Model')).toBe('ascending')
    // A page that does not answer (yet): the headers keep what it requests with.
    page.answers = false
    await wrapper.get('[data-test="sort-model"]').trigger('click')
    expect(ariaSort(wrapper, 'Model')).toBe('ascending')
  })

  it.each([
    ['admin errors', OpsErrorLogTable, 'admin.ops.errorLog.model', 'admin.ops.errorLog.status', 'admin.ops.errorLog.time'],
    ['user errors', UserErrorRequestsTable, 'usage.errors.model', 'usage.errors.status', 'usage.errors.time'],
  ] as const)('the %s table, status sent as status_code', async (_, Table, modelLabel, statusLabel, timeLabel) => {
    const page = reactive({ sortBy: 'created_at', sortOrder: 'desc' as 'asc' | 'desc', keys: ['model', 'status', 'created_at'] })
    const Host = defineComponent(() => () => h(Table as never, {
      rows: [], total: 0, loading: false, page: 1, pageSize: 20, sortBy: page.sortBy, sortOrder: page.sortOrder, visibleColumnKeys: page.keys,
      onSort: (sortBy: string, sortOrder: 'asc' | 'desc') => { page.sortBy = sortBy; page.sortOrder = sortOrder },
    }))
    const wrapper = mount(Host, { global: { stubs } })
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, timeLabel)).toBe('descending')
    await wrapper.get('[data-test="sort-status"]').trigger('click')
    expect(page.sortBy).toBe('status_code')
    expect(ariaSort(wrapper, statusLabel)).toBe('ascending')
    expect(ariaSort(wrapper, timeLabel)).toBe('none')
    await wrapper.get('[data-test="sort-model"]').trigger('click')
    expect([page.sortBy, page.sortOrder]).toEqual(['model', 'asc'])
    page.keys = ['status', 'created_at']
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, timeLabel)).toBe('none')
    expect(ariaSort(wrapper, statusLabel)).toBe('none')
    page.keys = ['model', 'status', 'created_at']
    await wrapper.vm.$nextTick()
    expect(ariaSort(wrapper, modelLabel)).toBe('ascending')
  })
})
