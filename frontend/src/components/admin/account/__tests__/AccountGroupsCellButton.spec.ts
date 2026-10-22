import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountGroupsCellButton from '../AccountGroupsCellButton.vue'
import type { Group } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
  }
})

const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai' }) as Group
const GroupBadgeStub = { props: ['name'], template: '<span data-test="badge">{{ name }}</span>' }

const mountCell = (groups: Group[]) => mount(AccountGroupsCellButton, {
  props: { groups, accountName: 'acc-1' },
  global: { stubs: { GroupBadge: GroupBadgeStub } }
})

describe('AccountGroupsCellButton', () => {
  it('is a single button with up to 3 badges and a plain +N', async () => {
    const wrapper = mountCell([group(1), group(2), group(3), group(4), group(5)])
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('type')).toBe('button')
    expect(buttons[0].attributes('aria-label')).toBe('admin.groupCategories.editAccountGroupsAria:{"name":"acc-1"}')
    expect(wrapper.findAll('[data-test="badge"]').map((badge) => badge.text())).toEqual(['Group 1', 'Group 2', 'Group 3'])
    expect(wrapper.text()).toContain('+2')

    await buttons[0].trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it('shows no +N for three groups or fewer', () => {
    const wrapper = mountCell([group(1), group(2), group(3)])
    expect(wrapper.findAll('[data-test="badge"]')).toHaveLength(3)
    expect(wrapper.text()).not.toContain('+')
  })

  it('shows a dash that still opens the dialog when there are no groups', async () => {
    const wrapper = mountCell([])
    expect(wrapper.get('button').text()).toBe('—')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })
})
