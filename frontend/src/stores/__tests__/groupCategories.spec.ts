import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useGroupCategoriesStore } from '../groupCategories'

const api = vi.hoisted(() => ({
  list: vi.fn(),
  save: vi.fn(),
  setGroupCategories: vi.fn()
}))

vi.mock('@/api/admin/groupCategories', () => ({ default: api }))

const category = (id: number, sort_order: number, group_ids: number[], name = `c${id}`) => ({ id, name, sort_order, group_ids })

beforeEach(() => {
  api.list.mockReset()
  api.save.mockReset()
  api.setGroupCategories.mockReset()
})

describe('group categories store', () => {
  it('shares one in-flight load, sorts by sort_order then id and loads only once', async () => {
    let resolve!: (value: unknown) => void
    api.list.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const store = useGroupCategoriesStore()

    const first = store.ensureLoaded()
    const second = store.ensureLoaded()
    expect(store.loadState).toBe('loading')
    resolve([category(5, 1, [1]), category(3, 1, []), category(9, 0, [2, 1])])
    await Promise.all([first, second])
    await store.ensureLoaded()

    expect(api.list).toHaveBeenCalledTimes(1)
    expect(store.loadState).toBe('ready')
    expect(store.categories.map((item) => item.id)).toEqual([9, 3, 5])
    expect(store.categoriesForGroup(1).map((item) => item.id)).toEqual([9, 5])
    expect(store.categoryIdsByGroup.get(2)).toEqual([9])
  })

  it('keeps the old list when a reload fails, never throwing', async () => {
    const store = useGroupCategoriesStore()
    api.list.mockResolvedValueOnce([category(1, 0, [7])])
    await store.reload()
    api.list.mockRejectedValueOnce(new Error('offline'))
    await expect(store.reload()).resolves.toBeUndefined()

    expect(store.loadState).toBe('failed')
    expect(store.categories.map((item) => item.id)).toEqual([1])
  })

  it('retries a failed ensureLoaded and shares the retry in flight', async () => {
    const store = useGroupCategoriesStore()
    api.list.mockRejectedValueOnce(new Error('offline'))
    await store.ensureLoaded()
    expect(store.loadState).toBe('failed')
    let resolve!: (value: unknown) => void
    api.list.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const retry = store.ensureLoaded()
    const shared = store.ensureLoaded()
    expect(api.list).toHaveBeenCalledTimes(2)
    resolve([category(1, 0, [])])
    await Promise.all([retry, shared])
    expect(store.loadState).toBe('ready')
    await store.ensureLoaded()
    expect(api.list).toHaveBeenCalledTimes(2)
  })

  it('matches filters leniently until the list is loaded', async () => {
    const store = useGroupCategoriesStore()
    expect(store.matchesFilter([1], '3')).toBe(true)
    api.list.mockResolvedValueOnce([category(3, 0, [1, 2]), category(4, 1, [])])
    await store.reload()

    expect(store.matchesFilter([1], '')).toBe(true)
    expect(store.matchesFilter([9, 2], '3')).toBe(true)
    expect(store.matchesFilter([9], '3')).toBe(false)
    expect(store.matchesFilter([], '4')).toBe(false)
    expect(store.matchesFilter([9], '404')).toBe(true)
  })

  it('patches membership locally from the server answer', async () => {
    const store = useGroupCategoriesStore()
    api.list.mockResolvedValueOnce([category(1, 0, [7, 8]), category(2, 1, [8]), category(3, 2, [])])
    await store.reload()
    api.setGroupCategories.mockResolvedValueOnce({ group_id: 7, category_ids: [3, 2] })

    expect(await store.setGroupCategories(7, [3, 2, 99])).toEqual([3, 2])
    expect(api.setGroupCategories).toHaveBeenCalledWith(7, [3, 2, 99])
    expect(store.categoriesForGroup(7).map((item) => item.id)).toEqual([2, 3])
    expect(store.categoriesForGroup(8).map((item) => item.id)).toEqual([1, 2])
  })

  it('replaces the list with the saved answer and lets save errors through', async () => {
    const store = useGroupCategoriesStore()
    api.save.mockResolvedValueOnce([category(2, 1, []), category(1, 0, [4])])
    await store.save({ items: [{ id: 1, name: 'c1' }, { name: 'c2' }], deleted_ids: [] })
    expect(store.categories.map((item) => item.id)).toEqual([1, 2])
    expect(store.loadState).toBe('ready')

    api.save.mockRejectedValueOnce({ status: 409, reason: 'GROUP_CATEGORY_NAME_DUPLICATE' })
    await expect(store.save({ items: [], deleted_ids: [] })).rejects.toMatchObject({ status: 409 })
    expect(store.categories.map((item) => item.id)).toEqual([1, 2])
  })

  it('copies categories to a duplicate, does nothing without categories and reports failures', async () => {
    const store = useGroupCategoriesStore()
    api.list.mockResolvedValueOnce([category(1, 0, [42]), category(2, 1, [42]), category(3, 2, [])])
    await store.reload()
    const onError = vi.fn()

    expect(await store.copyGroupCategories(5, 43, onError)).toBe(true)
    expect(api.setGroupCategories).not.toHaveBeenCalled()

    api.setGroupCategories.mockResolvedValueOnce({ group_id: 43, category_ids: [1, 2] })
    expect(await store.copyGroupCategories(42, 43, onError)).toBe(true)
    expect(api.setGroupCategories).toHaveBeenCalledWith(43, [1, 2])
    expect(store.categoriesForGroup(43).map((item) => item.id)).toEqual([1, 2])

    api.setGroupCategories.mockRejectedValueOnce({ message: 'group not found' })
    expect(await store.copyGroupCategories(42, 44, onError)).toBe(false)
    expect(onError).toHaveBeenCalledWith('group not found')
  })

  it('loads the list before copying and reports a load failure', async () => {
    const store = useGroupCategoriesStore()
    const onError = vi.fn()
    api.list.mockRejectedValueOnce({ message: 'list failed' })

    expect(await store.copyGroupCategories(42, 43, onError)).toBe(false)
    expect(onError).toHaveBeenCalledWith('list failed')
    expect(api.setGroupCategories).not.toHaveBeenCalled()
  })
})
