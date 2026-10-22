import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useGroupCategoriesStore } from '@/stores/groupCategories'
import GroupCategoryField from '../GroupCategoryField.vue'

const api = vi.hoisted(() => ({ list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() }))
const showWarning = vi.hoisted(() => vi.fn())

vi.mock('@/api/admin/groupCategories', () => ({ default: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showWarning }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

const categories = [
  { id: 1, name: 'Public', sort_order: 0, group_ids: [7] },
  { id: 2, name: 'Monitor', sort_order: 1, group_ids: [] },
  { id: 3, name: 'Dedicated', sort_order: 2, group_ids: [7] }
]

type FieldVM = { save: (groupId: number) => Promise<boolean> }

async function mountField(groupId?: number) {
  const wrapper = mount(GroupCategoryField, { props: { groupId } })
  await flushPromises()
  return wrapper
}

const checkbox = (wrapper: Awaited<ReturnType<typeof mountField>>, id: number) =>
  wrapper.get<HTMLInputElement>(`input[data-category-id="${id}"]`)

beforeEach(() => {
  api.list.mockReset()
  api.setGroupCategories.mockReset()
  showWarning.mockReset()
  api.list.mockResolvedValue(categories)
})

describe('GroupCategoryField', () => {
  it('preselects the categories of the edited group', async () => {
    const wrapper = await mountField(7)
    expect(checkbox(wrapper, 1).element.checked).toBe(true)
    expect(checkbox(wrapper, 2).element.checked).toBe(false)
    expect(checkbox(wrapper, 3).element.checked).toBe(true)
  })

  it('keeps ticks made while an existing category list is reloading', async () => {
    const store = useGroupCategoriesStore()
    await store.reload()
    let resolve!: (value: typeof categories) => void
    api.list.mockImplementationOnce(() => new Promise((done) => { resolve = done }))
    const reload = store.reload()
    const wrapper = mount(GroupCategoryField)
    await checkbox(wrapper, 2).setValue(true)
    resolve(categories)
    await reload
    await flushPromises()

    expect(checkbox(wrapper, 2).element.checked).toBe(true)
    api.setGroupCategories.mockResolvedValueOnce({ group_id: 8, category_ids: [2] })
    expect(await (wrapper.vm as unknown as FieldVM).save(8)).toBe(true)
    expect(api.setGroupCategories).toHaveBeenCalledWith(8, [2])
  })

  it('sends no request when nothing changed', async () => {
    const wrapper = await mountField(7)
    expect(await (wrapper.vm as unknown as FieldVM).save(7)).toBe(true)
    const created = await mountField()
    expect(await (created.vm as unknown as FieldVM).save(8)).toBe(true)
    expect(api.setGroupCategories).not.toHaveBeenCalled()
  })

  it('saves a change in the admin order for the given group', async () => {
    api.setGroupCategories.mockResolvedValueOnce({ group_id: 7, category_ids: [2, 3] })
    const wrapper = await mountField(7)
    await checkbox(wrapper, 1).setValue(false)
    await checkbox(wrapper, 2).setValue(true)

    expect(await (wrapper.vm as unknown as FieldVM).save(7)).toBe(true)
    expect(api.setGroupCategories).toHaveBeenCalledWith(7, [2, 3])
  })

  it('warns and returns false when the save fails, without throwing', async () => {
    api.setGroupCategories.mockRejectedValueOnce({ message: 'boom' })
    const wrapper = await mountField()
    await checkbox(wrapper, 2).setValue(true)

    expect(await (wrapper.vm as unknown as FieldVM).save(9)).toBe(false)
    expect(api.setGroupCategories).toHaveBeenCalledWith(9, [2])
    expect(showWarning).toHaveBeenCalledWith('admin.groupCategories.groupSavedCategoriesFailed:{"message":"boom"}')
  })

  it('shows the empty hint when there are no categories', async () => {
    api.list.mockResolvedValue([])
    const wrapper = await mountField()
    expect(wrapper.text()).toContain('admin.groupCategories.fieldEmpty')
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(false)
  })
})
