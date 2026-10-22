import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import groupCategoriesAPI from '@/api/admin/groupCategories'
import type { GroupCategory, GroupCategorySavePayload } from '@/api/admin/groupCategories'
import { extractApiErrorMessage } from '@/utils/apiError'

export type GroupCategoryLoadState = 'idle' | 'loading' | 'ready' | 'failed'

const sortCategories = (list: GroupCategory[]): GroupCategory[] =>
  list
    .map((category) => ({ ...category, group_ids: category.group_ids ?? [] }))
    .sort((left, right) => left.sort_order - right.sort_order || left.id - right.id)

/**
 * 分组分类（XY2API）：后台整理分组用的标签，网关、调度、计费都不读。
 * 分类列表带每个分类的 group_ids，页面按分组 id 关联，所以停用的分组也能对上分类。
 * 不从 stores/index.ts 导出，测试里 mock '@/stores' 的工厂不受影响。
 */
export const useGroupCategoriesStore = defineStore('groupCategories', () => {
  const categories = ref<GroupCategory[]>([])
  const loadState = ref<GroupCategoryLoadState>('idle')
  let inflight: Promise<void> | null = null

  const categoryIdsByGroup = computed(() => {
    const byGroup = new Map<number, number[]>()
    for (const category of categories.value) {
      for (const groupId of category.group_ids) {
        const ids = byGroup.get(groupId)
        if (ids) ids.push(category.id)
        else byGroup.set(groupId, [category.id])
      }
    }
    return byGroup
  })

  /** Categories of a group in the admin's order. */
  function categoriesForGroup(groupId: number): GroupCategory[] {
    const ids = new Set(categoryIdsByGroup.value.get(groupId) ?? [])
    return categories.value.filter((category) => ids.has(category.id))
  }

  async function fetchList() {
    categories.value = sortCategories(await groupCategoriesAPI.list())
    loadState.value = 'ready'
  }

  /** Reloads the list; never throws. A failed load keeps the old list. */
  function reload(): Promise<void> {
    if (inflight) return inflight
    if (loadState.value !== 'ready') loadState.value = 'loading'
    inflight = (async () => {
      try {
        await fetchList()
      } catch {
        loadState.value = 'failed'
      } finally {
        inflight = null
      }
    })()
    return inflight
  }

  /** Loads once; later calls share the request or do nothing. */
  function ensureLoaded(): Promise<void> {
    if (inflight) return inflight
    if (loadState.value !== 'idle') return Promise.resolve()
    return reload()
  }

  /**
   * Local re-check of a list filter: true for no filter, while the list is not
   * loaded, or for an unknown category; the server stays the authority.
   */
  function matchesFilter(groupIds: readonly number[], value: string | null | undefined): boolean {
    if (!value || loadState.value !== 'ready') return true
    const category = categories.value.find((item) => String(item.id) === value)
    if (!category) return true
    const members = new Set(category.group_ids)
    return groupIds.some((id) => members.has(id))
  }

  /** Saves the whole list and replaces it with the server's answer; throws on error. */
  async function save(payload: GroupCategorySavePayload): Promise<void> {
    categories.value = sortCategories(await groupCategoriesAPI.save(payload))
    loadState.value = 'ready'
  }

  /** Replaces one group's categories and patches membership locally; throws on error. */
  async function setGroupCategories(groupId: number, categoryIds: number[]): Promise<number[]> {
    const result = await groupCategoriesAPI.setGroupCategories(groupId, categoryIds)
    const saved = new Set(result?.category_ids ?? [])
    categories.value = categories.value.map((category) => {
      const others = category.group_ids.filter((id) => id !== groupId)
      return { ...category, group_ids: saved.has(category.id) ? [...others, groupId] : others }
    })
    return [...saved]
  }

  /**
   * Gives a duplicated group the source group's categories. Does nothing when
   * the source has none; reports a failure through onError and never throws,
   * because the duplicate itself has already been created.
   */
  async function copyGroupCategories(fromId: number, toId: number, onError?: (message: string) => void): Promise<boolean> {
    try {
      if (inflight) await inflight
      // 没加载成功时直接再取一次，失败就带上服务端的错误信息
      if (loadState.value !== 'ready') await fetchList()
      const ids = categoryIdsByGroup.value.get(fromId) ?? []
      if (ids.length === 0) return true
      await setGroupCategories(toId, ids)
      return true
    } catch (error) {
      onError?.(extractApiErrorMessage(error, ''))
      return false
    }
  }

  return {
    categories,
    loadState,
    categoryIdsByGroup,
    categoriesForGroup,
    reload,
    ensureLoaded,
    matchesFilter,
    save,
    setGroupCategories,
    copyGroupCategories
  }
})
