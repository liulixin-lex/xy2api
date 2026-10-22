import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupCategoryFilter from '../GroupCategoryFilter.vue'

const authState = vi.hoisted(() => ({ isSimpleMode: false }))
const api = vi.hoisted(() => ({ list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() }))

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))
vi.mock('@/api/admin/groupCategories', () => ({ default: api }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const SelectStub = {
  props: ['modelValue', 'options'],
  emits: ['update:modelValue', 'change'],
  template: '<div data-test="select">{{ options.map((option) => option.value + ":" + option.label).join("|") }}</div>'
}

function mountFilter(modelValue = '') {
  return mount(GroupCategoryFilter, {
    props: { modelValue, 'onUpdate:modelValue': () => {} },
    global: { stubs: { Select: SelectStub } }
  })
}

beforeEach(() => {
  authState.isSimpleMode = false
  api.list.mockReset()
})

describe('GroupCategoryFilter', () => {
  it('stays hidden with no categories and no value, and lists categories in order otherwise', async () => {
    api.list.mockResolvedValueOnce([])
    const empty = mountFilter()
    await flushPromises()
    expect(empty.find('[data-test="group-category-filter"]').exists()).toBe(false)

    api.list.mockResolvedValueOnce([
      { id: 4, name: 'Monitor', sort_order: 1, group_ids: [] },
      { id: 1, name: 'Public', sort_order: 0, group_ids: [3] }
    ])
    const wrapper = mountFilter()
    await flushPromises()
    expect(wrapper.get('[data-test="group-category-filter"]').text()).toBe(':admin.groupCategories.allCategories|1:Public|4:Monitor')
  })

  it('reloads on mount and resets a deleted selection after a successful load', async () => {
    api.list.mockResolvedValueOnce([{ id: 1, name: 'Public', sort_order: 0, group_ids: [] }])
    const wrapper = mountFilter('9')
    expect(wrapper.find('[data-test="group-category-filter"]').exists()).toBe(true)
    await flushPromises()

    expect(api.list).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('update:modelValue')).toEqual([['']])
    expect(wrapper.emitted('change')).toEqual([[]])
  })

  it('keeps the selection when the load fails', async () => {
    api.list.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountFilter('9')
    await flushPromises()

    expect(wrapper.find('[data-test="group-category-filter"]').exists()).toBe(true)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.emitted('change')).toBeUndefined()
  })

  it('renders nothing and loads nothing in simple mode', async () => {
    authState.isSimpleMode = true
    const wrapper = mountFilter('1')
    await flushPromises()
    expect(wrapper.find('[data-test="group-category-filter"]').exists()).toBe(false)
    expect(api.list).not.toHaveBeenCalled()
  })
})
