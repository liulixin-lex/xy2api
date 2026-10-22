import { defineComponent, h, nextTick, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useGroupCategoryTabs } from '../useGroupCategoryTabs'
import { useGroupCategoriesStore } from '@/stores/groupCategories'

const authState = vi.hoisted(() => ({ isSimpleMode: false }))
const api = vi.hoisted(() => ({ list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() }))

vi.mock('@/stores', () => ({ useAuthStore: () => authState }))
vi.mock('@/api/admin/groupCategories', () => ({ default: api }))

const categories = [
  { id: 1, name: 'Public', sort_order: 0, group_ids: [10, 11] },
  { id: 2, name: 'Monitor', sort_order: 1, group_ids: [20] },
  { id: 3, name: 'Empty', sort_order: 2, group_ids: [] }
]

function mountTabs(enabled = true, eligibleIds = [10, 11, 20]) {
  const eligible = ref(eligibleIds.map((id) => ({ id })))
  let tabs!: ReturnType<typeof useGroupCategoryTabs>
  const wrapper = mount(defineComponent({
    setup() {
      tabs = useGroupCategoryTabs(() => enabled, () => eligible.value)
      return () => h('div')
    }
  }))
  return { wrapper, eligible, tabs: () => tabs }
}

beforeEach(() => {
  authState.isSimpleMode = false
  api.list.mockReset()
  api.list.mockResolvedValue(categories)
})

describe('useGroupCategoryTabs', () => {
  it('lists only categories that hold an eligible group and loads the store', async () => {
    const { tabs } = mountTabs(true, [10, 11])
    await flushPromises()
    expect(api.list).toHaveBeenCalledTimes(1)
    expect(tabs().tabs.value.map((tab) => tab.id)).toEqual([1])
  })

  it('has no tabs and loads nothing when disabled or in simple mode', async () => {
    const disabled = mountTabs(false)
    await flushPromises()
    expect(disabled.tabs().tabs.value).toEqual([])

    authState.isSimpleMode = true
    const simple = mountTabs(true)
    await flushPromises()
    expect(simple.tabs().tabs.value).toEqual([])
    expect(api.list).not.toHaveBeenCalled()
  })

  it('toggles between one tab and none', async () => {
    const { tabs } = mountTabs()
    await flushPromises()
    tabs().toggle(2)
    expect(tabs().activeId.value).toBe(2)
    expect([...tabs().activeGroupIds.value!]).toEqual([20])
    tabs().toggle(1)
    expect(tabs().activeId.value).toBe(1)
    tabs().toggle(1)
    expect(tabs().activeId.value).toBeNull()
    expect(tabs().activeGroupIds.value).toBeNull()
  })

  it('resets the active tab when it disappears', async () => {
    const { tabs, eligible } = mountTabs()
    await flushPromises()
    tabs().toggle(2)
    eligible.value = [{ id: 10 }]
    await nextTick()
    expect(tabs().activeId.value).toBeNull()

    tabs().toggle(1)
    const store = useGroupCategoriesStore()
    store.categories = store.categories.filter((category) => category.id !== 1)
    await nextTick()
    expect(tabs().activeId.value).toBeNull()
  })
})
