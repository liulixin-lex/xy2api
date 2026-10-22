/**
 * Admin group categories API (XY2API).
 *
 * Categories are admin-only labels for organising groups: routing,
 * scheduling, billing and user-facing responses never read them. A group can
 * be in several categories or none.
 */

import { apiClient } from '../client'

export interface GroupCategory {
  id: number
  name: string
  sort_order: number
  /** Live groups in this category, in group sort order. */
  group_ids: number[]
}

/** An item without id creates a category; with id it renames one. */
export interface GroupCategorySaveItem {
  id?: number
  name: string
}

export interface GroupCategorySavePayload {
  /** The order of items becomes the category order. */
  items: GroupCategorySaveItem[]
  /** Only these categories are deleted; others not listed are kept. */
  deleted_ids: number[]
}

export interface GroupCategoryMembership {
  group_id: number
  category_ids: number[]
}

const base = '/admin/group-categories'

export async function list(): Promise<GroupCategory[]> {
  const { data } = await apiClient.get<GroupCategory[]>(base)
  return data ?? []
}

/** Saves the whole category list at once and returns the fresh list. */
export async function save(payload: GroupCategorySavePayload): Promise<GroupCategory[]> {
  const { data } = await apiClient.put<GroupCategory[]>(base, payload)
  return data ?? []
}

/** Replaces one group's categories; unknown ids are dropped by the server. */
export async function setGroupCategories(groupId: number, categoryIds: number[]): Promise<GroupCategoryMembership> {
  const { data } = await apiClient.put<GroupCategoryMembership>(`${base}/groups/${groupId}`, { category_ids: categoryIds })
  return data
}

export const groupCategoriesAPI = {
  list,
  save,
  setGroupCategories
}

export default groupCategoriesAPI
