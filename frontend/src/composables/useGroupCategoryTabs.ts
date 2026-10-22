import { computed, ref, watch } from 'vue'
import { useAuthStore } from '@/stores'
import { useGroupCategoriesStore } from '@/stores/groupCategories'

/**
 * 分组选择面板的分类标签（XY2API）：点一个标签只看这个分类的分组，再点一次恢复全部。
 * 只列出含有可选分组的分类；简单模式或未开启时没有标签。
 * 筛选只返回分组 id 集合，由调用方在自己的列表里过滤，已勾选的值不受影响。
 */
export function useGroupCategoryTabs(enabled: () => boolean, eligible: () => { id: number }[]) {
  const store = useGroupCategoriesStore()
  const authStore = useAuthStore()
  const active = computed(() => enabled() && !authStore.isSimpleMode)
  const activeId = ref<number | null>(null)

  const tabs = computed(() => {
    if (!active.value) return []
    const ids = new Set(eligible().map((group) => group.id))
    return store.categories.filter((category) => category.group_ids.some((id) => ids.has(id)))
  })

  const activeGroupIds = computed<Set<number> | null>(() => {
    if (activeId.value === null) return null
    const tab = tabs.value.find((category) => category.id === activeId.value)
    return tab ? new Set(tab.group_ids) : null
  })

  // 当前标签消失（分类被删、可选分组变了）时回到全部
  watch(tabs, (list) => {
    if (activeId.value !== null && !list.some((category) => category.id === activeId.value)) activeId.value = null
  })

  watch(active, (on) => {
    if (on) void store.ensureLoaded()
  }, { immediate: true })

  const toggle = (id: number) => {
    activeId.value = activeId.value === id ? null : id
  }

  return { tabs, activeId, activeGroupIds, toggle }
}
