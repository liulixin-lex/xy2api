import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupCategoryManagerButton from '../GroupCategoryManagerButton.vue'

const { api, showError, showSuccess } = vi.hoisted(() => ({
  api: { list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() },
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin/groupCategories', () => ({ default: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

const BaseDialogStub = defineComponent({
  props: ['show'],
  emits: ['close'],
  template: '<div v-if="show" data-test="dialog"><button data-test="dialog-x" @click="$emit(\'close\')">x</button><slot /><slot name="footer" /></div>'
})
const ConfirmDialogStub = defineComponent({
  props: ['show', 'message'],
  emits: ['confirm', 'cancel'],
  template: '<div v-if="show" data-test="delete-confirm">{{ message }}<button data-test="delete-yes" @click="$emit(\'confirm\')">yes</button><button data-test="delete-no" @click="$emit(\'cancel\')">no</button></div>'
})
const DraggableStub = defineComponent({
  name: 'VueDraggable',
  props: ['modelValue'],
  emits: ['update:modelValue'],
  template: '<div><slot /></div>'
})

const categories = [
  { id: 1, name: 'Public', sort_order: 0, group_ids: [3, 4] },
  { id: 2, name: 'Monitor', sort_order: 1, group_ids: [] },
  { id: 3, name: 'Old', sort_order: 2, group_ids: [] }
]

async function openManager() {
  const wrapper = mount(GroupCategoryManagerButton, {
    global: { stubs: { BaseDialog: BaseDialogStub, ConfirmDialog: ConfirmDialogStub, VueDraggable: DraggableStub, Icon: true } }
  })
  await wrapper.get('[data-test="group-category-manage"]').trigger('click')
  await flushPromises()
  return wrapper
}

type Wrapper = Awaited<ReturnType<typeof openManager>>
const rows = (wrapper: Wrapper) => wrapper.findAll('[data-test="group-category-row"]')
const rowNames = (wrapper: Wrapper) => rows(wrapper).map((row) => (row.get('input').element as HTMLInputElement).value)
const save = (wrapper: Wrapper) => wrapper.get('[data-test="group-category-save"]').trigger('click')

beforeEach(() => {
  for (const fn of [api.list, api.save, showError, showSuccess]) fn.mockReset()
  api.list.mockResolvedValue(categories)
  api.save.mockImplementation(async (payload: { items: { id?: number; name: string }[] }) =>
    payload.items.map((item, index) => ({ id: item.id ?? 100 + index, name: item.name, sort_order: index, group_ids: [] })))
})

describe('GroupCategoryManagerButton', () => {
  it('reloads on open and saves add, rename, drag and delete as one request', async () => {
    const wrapper = await openManager()
    expect(api.list).toHaveBeenCalledTimes(1)
    expect(rowNames(wrapper)).toEqual(['Public', 'Monitor', 'Old'])
    expect(rows(wrapper)[0].text()).toContain('admin.groupCategories.groupCount:{"count":2}')

    await rows(wrapper)[0].get('input').setValue('  Public groups ')
    await rows(wrapper)[2].get('[data-test="group-category-remove"]').trigger('click')
    await wrapper.get('[data-test="group-category-add"] input').setValue('Dedicated')
    await wrapper.get('[data-test="group-category-add"]').trigger('submit')
    const draggable = wrapper.getComponent(DraggableStub)
    const draft = draggable.props('modelValue') as unknown[]
    draggable.vm.$emit('update:modelValue', [draft[1], draft[0], draft[2]])
    await flushPromises()
    expect(rowNames(wrapper)).toEqual(['Monitor', '  Public groups ', 'Dedicated'])

    await save(wrapper)
    await flushPromises()

    expect(wrapper.find('[data-test="delete-confirm"]').exists()).toBe(false)
    expect(api.save).toHaveBeenCalledWith({
      items: [{ id: 2, name: 'Monitor' }, { id: 1, name: 'Public groups' }, { name: 'Dedicated' }],
      deleted_ids: [3]
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.groupCategories.saved')
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
  })

  it('blocks the save with an inline error for an invalid draft', async () => {
    const wrapper = await openManager()
    await rows(wrapper)[2].get('input').setValue('public')
    await save(wrapper)

    expect(api.save).not.toHaveBeenCalled()
    expect(rows(wrapper)[2].text()).toContain('admin.groupCategories.nameDuplicate')

    await rows(wrapper)[2].get('input').setValue('')
    await save(wrapper)
    expect(rows(wrapper)[2].text()).toContain('admin.groupCategories.nameRequired')
    expect(api.save).not.toHaveBeenCalled()
  })

  it('asks for confirmation only when a deleted category still has groups', async () => {
    const wrapper = await openManager()
    await rows(wrapper)[0].get('[data-test="group-category-remove"]').trigger('click')
    await save(wrapper)

    const dialog = wrapper.get('[data-test="delete-confirm"]')
    expect(dialog.text()).toContain('admin.groupCategories.deleteConfirm:{"names":"Public","count":2}')
    expect(api.save).not.toHaveBeenCalled()

    await wrapper.get('[data-test="delete-no"]').trigger('click')
    expect(api.save).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(true)

    await save(wrapper)
    await wrapper.get('[data-test="delete-yes"]').trigger('click')
    await flushPromises()
    expect(api.save).toHaveBeenCalledWith({ items: [{ id: 2, name: 'Monitor' }, { id: 3, name: 'Old' }], deleted_ids: [1] })
  })

  it('discards the draft on cancel without sending anything', async () => {
    const wrapper = await openManager()
    await rows(wrapper)[0].get('input').setValue('Changed')
    await wrapper.get('[data-test="dialog-x"]').trigger('click')
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
    expect(api.save).not.toHaveBeenCalled()

    await wrapper.get('[data-test="group-category-manage"]').trigger('click')
    await flushPromises()
    expect(rowNames(wrapper)).toEqual(['Public', 'Monitor', 'Old'])
  })

  it('keeps the dialog and its draft when the server rejects the save', async () => {
    api.save.mockReset()
    api.save.mockRejectedValueOnce({ status: 409, reason: 'GROUP_CATEGORY_NAME_DUPLICATE', message: 'duplicate' })
    const wrapper = await openManager()
    await rows(wrapper)[1].get('input').setValue('Monitoring')
    await save(wrapper)
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.groupCategories.nameDuplicate')
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(true)
    expect(rowNames(wrapper)).toEqual(['Public', 'Monitoring', 'Old'])
  })
})
