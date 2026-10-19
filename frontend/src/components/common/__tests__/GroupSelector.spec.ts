import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupSelector from '../GroupSelector.vue'

const authState = { isSimpleMode: false }

vi.mock('@/stores', () => ({ useAuthStore: () => authState }))
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
