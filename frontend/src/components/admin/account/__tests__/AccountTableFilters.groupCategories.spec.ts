import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import AccountTableFilters from '../AccountTableFilters.vue'
import Select from '@/components/common/Select.vue'
import type { AdminGroup } from '@/types'

const categoriesApi = vi.hoisted(() => ({ list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() }))
vi.mock('@/api/admin/groupCategories', () => ({ default: categoriesApi }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: { count: number }) => params ? `${key} (${params.count})` : key
    })
  }
})

const defaultFilters = () => ({
  platform: '', type: '', status: '', privacy_mode: '', iq_status: '', group: '',
  search: 'existing search', lite: '1', sort_by: 'priority', sort_order: 'asc'
})

function mountFilters(filters: Record<string, unknown> = defaultFilters()) {
  return mount(AccountTableFilters, {
    attachTo: document.body,
    props: {
      searchQuery: 'existing search', filters,
      groups: [{ id: 42, name: 'A very long group name' } as AdminGroup]
    },
    global: { stubs: { Teleport: true } }
  })
}

enableAutoUnmount(afterEach)
beforeEach(() => {
  categoriesApi.list.mockReset()
  categoriesApi.list.mockResolvedValue([])
})
afterEach(() => {
  vi.useRealTimers()
  document.body.innerHTML = ''
})

describe('AccountTableFilters group categories', () => {
  it('shows the category filter before the group filter once categories exist and emits a merged snapshot', async () => {
    categoriesApi.list.mockResolvedValue([
      { id: 3, name: 'Public', sort_order: 0, group_ids: [42] },
      { id: 5, name: 'Monitor', sort_order: 1, group_ids: [] }
    ])
    const filters = { ...defaultFilters(), platform: 'openai', group: '42' }
    const wrapper = mountFilters(filters)
    await flushPromises()

    const selects = wrapper.findAllComponents(Select)
    expect(selects.map(select => select.get('button').attributes('aria-label')).slice(-2)).toEqual([
      'admin.groupCategories.allCategories',
      'admin.accounts.allGroups'
    ])
    const categoryFilter = selects[5]
    expect(categoryFilter.props('options').map(option => option.value)).toEqual(['', '3', '5'])
    expect(categoryFilter.classes()).toEqual(expect.arrayContaining(['min-w-0', 'w-full', 'sm:w-44']))

    await categoryFilter.get('button').trigger('click')
    await categoryFilter.findAll('[role="option"]')[2].trigger('click')
    expect(wrapper.emitted('update:filters')?.at(-1)).toEqual([{ ...filters, group_category: '5' }])
    expect(wrapper.emitted('change')).toHaveLength(1)
  })
})
