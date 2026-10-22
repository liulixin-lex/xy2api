import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupSelector from '../GroupSelector.vue'

const authState = { isSimpleMode: false }
const categoriesApi = vi.hoisted(() => ({ list: vi.fn(), save: vi.fn(), setGroupCategories: vi.fn() }))

vi.mock('@/stores', () => ({ useAuthStore: () => authState }))
vi.mock('@/api/admin/groupCategories', () => ({ default: categoriesApi }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const groups = [
  { id: 1, name: 'Basic', platform: 'anthropic', status: 'active' },
  { id: 2, name: 'Composite', platform: 'composite', status: 'active' }
] as any

const mountSelector = (modelValue: number[] = []) => mount(GroupSelector, {
  props: { modelValue, groups },
  global: { stubs: { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' }, Icon: true } }
})

describe('GroupSelector simple-mode binding policy', () => {
  beforeEach(() => { authState.isSimpleMode = false })

  it('hides composite groups in simple mode and preserves basic groups', () => {
    authState.isSimpleMode = true
    const wrapper = mountSelector()
    expect(wrapper.text()).toContain('Basic')
    expect(wrapper.text()).not.toContain('Composite')
  })

  it('keeps composite groups available in advanced mode', () => {
    const wrapper = mountSelector()
    expect(wrapper.text()).toContain('Composite')
  })

  it('cleans hidden historical composite IDs while preserving visible selections', () => {
    authState.isSimpleMode = true
    const wrapper = mountSelector([1, 2])
    expect(wrapper.emitted('update:modelValue')).toEqual([[[1]]])
  })
})

describe('GroupSelector exclusive tag', () => {
  beforeEach(() => { authState.isSimpleMode = false })

  it('tags exclusive groups in purple even when their user-facing badge is hidden', () => {
    const wrapper = mount(GroupSelector, {
      props: {
        modelValue: [],
        groups: [
          { id: 1, name: 'Public', platform: 'openai', status: 'active', is_exclusive: false },
          { id: 2, name: 'VIP', platform: 'openai', status: 'active', is_exclusive: true, show_exclusive_badge: false }
        ] as any
      },
      global: { stubs: { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' }, Icon: true } }
    })

    const tags = wrapper.findAll('[data-test="group-exclusive-tag"]')
    expect(tags).toHaveLength(1)
    expect(tags[0].text()).toBe('admin.groups.exclusive')
    expect(tags[0].classes()).toContain('text-purple-600')
    expect(tags[0].element.closest('label')?.textContent).toContain('VIP')
  })
})

describe('GroupSelector category tabs', () => {
  const stubs = { GroupBadge: { props: ['name'], template: '<span>{{ name }}</span>' }, Icon: true }
  const pickerGroups = [
    { id: 1, name: 'Alpha', platform: 'openai', status: 'active' },
    { id: 2, name: 'Beta', platform: 'openai', status: 'active' },
    { id: 3, name: 'Gamma', platform: 'openai', status: 'active' },
    { id: 4, name: 'Claude', platform: 'anthropic', status: 'active' },
    { id: 5, name: 'Mix', platform: 'composite', status: 'active' }
  ] as any
  const categories = [
    { id: 10, name: 'Public', sort_order: 0, group_ids: [1, 2] },
    { id: 11, name: 'Monitor', sort_order: 1, group_ids: [3, 5] },
    { id: 12, name: 'Claude only', sort_order: 2, group_ids: [4] }
  ]

  const mountPicker = async (props: Record<string, unknown> = {}) => {
    // The immediate simple-mode cleanup emits during mount, before the wrapper exists.
    const holder: { wrapper?: ReturnType<typeof mount> } = {}
    const wrapper = mount(GroupSelector, {
      props: {
        modelValue: [],
        groups: pickerGroups,
        platform: 'openai',
        'onUpdate:modelValue': (value: number[]) => holder.wrapper?.setProps({ modelValue: value }),
        ...props
      },
      global: { stubs }
    })
    holder.wrapper = wrapper
    await flushPromises()
    return wrapper
  }
  const groupList = (wrapper: Awaited<ReturnType<typeof mountPicker>>) =>
    wrapper.findAll('div').find((div) => div.classes().includes('grid'))!
  const visibleNames = (wrapper: Awaited<ReturnType<typeof mountPicker>>) =>
    groupList(wrapper).findAll('label').map((label) => label.findAll('span')[0].text())
  const tab = (wrapper: Awaited<ReturnType<typeof mountPicker>>, name: string) =>
    wrapper.findAll('[role="tab"]').find((item) => item.text() === name)!

  beforeEach(() => {
    authState.isSimpleMode = false
    categoriesApi.list.mockReset()
    categoriesApi.list.mockResolvedValue(categories)
  })

  it('renders exactly as before without the prop or without categories', async () => {
    const plain = await mountPicker()
    expect(plain.find('[data-test="group-category-tabs"]').exists()).toBe(false)
    expect(plain.find('input[type="text"]').exists()).toBe(false)
    expect(groupList(plain).classes()).toEqual(expect.arrayContaining(['grid', 'max-h-32', 'grid-cols-2', 'rounded-lg']))
    expect(categoriesApi.list).not.toHaveBeenCalled()

    categoriesApi.list.mockResolvedValue([])
    const enabled = await mountPicker({ categoryTabs: true })
    expect(categoriesApi.list).toHaveBeenCalledTimes(1)
    expect(enabled.html()).toBe(plain.html())
  })

  it('shows only categories with selectable groups, with the search row even for 5 groups or fewer', async () => {
    const wrapper = await mountPicker({ categoryTabs: true })
    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs.map((item) => item.text())).toEqual(['Public', 'Monitor'])
    expect(tabs.every((item) => item.attributes('type') === 'button')).toBe(true)
    expect(wrapper.find('input[type="text"]').exists()).toBe(true)
    expect(wrapper.get('[role="tablist"]').element.parentElement).toBe(wrapper.get('input[type="text"]').element.parentElement)
  })

  it('filters by a tab and shows all groups again on a second click', async () => {
    const wrapper = await mountPicker({ categoryTabs: true })
    expect(visibleNames(wrapper)).toEqual(['Alpha', 'Beta', 'Gamma', 'Mix'])

    await tab(wrapper, 'Monitor').trigger('click')
    expect(tab(wrapper, 'Monitor').attributes('aria-selected')).toBe('true')
    expect(visibleNames(wrapper)).toEqual(['Gamma', 'Mix'])

    await tab(wrapper, 'Monitor').trigger('click')
    expect(tab(wrapper, 'Monitor').attributes('aria-selected')).toBe('false')
    expect(visibleNames(wrapper)).toEqual(['Alpha', 'Beta', 'Gamma', 'Mix'])
  })

  it('combines the search text with the active tab', async () => {
    const wrapper = await mountPicker({ categoryTabs: true })
    await tab(wrapper, 'Public').trigger('click')
    await wrapper.get('input[type="text"]').setValue('bet')
    expect(visibleNames(wrapper)).toEqual(['Beta'])
  })

  it('keeps ticks made under other category tabs', async () => {
    const wrapper = await mountPicker({ categoryTabs: true, modelValue: [3] })
    await tab(wrapper, 'Public').trigger('click')
    await wrapper.findAll('input[type="checkbox"]')[1].setValue(true)
    await tab(wrapper, 'Monitor').trigger('click')
    await wrapper.findAll('input[type="checkbox"]')[1].setValue(true)

    expect(wrapper.emitted('update:modelValue')).toEqual([[[3, 2]], [[3, 2, 5]]])
    await tab(wrapper, 'Monitor').trigger('click')
    const checked = wrapper.findAll('input[type="checkbox"]').map((input) => (input.element as HTMLInputElement).checked)
    expect(checked).toEqual([false, true, true, true])
  })

  it('shows no tabs in simple mode and still drops only composite ids', async () => {
    authState.isSimpleMode = true
    const wrapper = await mountPicker({ categoryTabs: true, modelValue: [1, 5] })
    expect(wrapper.find('[role="tab"]').exists()).toBe(false)
    expect(categoriesApi.list).not.toHaveBeenCalled()
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[1]])
  })

  it('uses the given list height', async () => {
    const wrapper = await mountPicker({ categoryTabs: true, listClass: 'max-h-80' })
    const list = groupList(wrapper)
    expect(list.classes()).toContain('max-h-80')
    expect(list.classes()).not.toContain('max-h-32')
  })
})
