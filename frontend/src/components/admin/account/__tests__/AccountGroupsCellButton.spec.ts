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
const GroupBadgeStub = { props: { name: String, showPlatformIcon: Boolean }, template: '<span data-test="badge" :data-icon="String(showPlatformIcon)">{{ name }}</span>' }

const mountCell = (groups: Group[]) => mount(AccountGroupsCellButton, {
  props: { groups, accountName: 'acc-1' },
  global: { stubs: { GroupBadge: GroupBadgeStub } }
})

describe('AccountGroupsCellButton', () => {
  it('is a single button with up to 2 badges and a plain +N, without a native title or platform icons', async () => {
    const wrapper = mountCell([group(1), group(2), group(3), group(4), group(5)])
    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('type')).toBe('button')
    expect(buttons[0].attributes('aria-label')).toBe('admin.groupCategories.editAccountGroupsAria:{"name":"acc-1"}')
    expect(buttons[0].attributes('title')).toBeUndefined()
    expect(wrapper.findAll('[data-test="badge"]').map((badge) => [badge.text(), badge.attributes('title'), badge.attributes('data-icon')])).toEqual([['Group 1', undefined, 'false'], ['Group 2', undefined, 'false']])
    expect(wrapper.text()).toContain('+3')

    await buttons[0].trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })

  it('shows no +N for two groups or fewer, and +1 for a third', () => {
    const wrapper = mountCell([group(1), group(2)])
    expect(wrapper.findAll('[data-test="badge"]')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('+')
    expect(mountCell([group(1), group(2), group(3)]).text()).toContain('+1')
  })

  // Scenario 34: membership badges carry their group's platform colour (D25), no border and no icon.
  it('colours each badge by its group\'s platform', () => {
    const platforms = ['openai', 'anthropic', 'gemini', 'antigravity', 'grok']
    const colours = platforms.map((platform) => {
      const wrapper = mount(AccountGroupsCellButton, { props: { groups: [{ ...group(1), platform } as Group], accountName: 'acc-1' } })
      const badge = wrapper.get('.max-w-\\[8\\.5em\\]')
      expect(badge.find('svg').exists()).toBe(false)
      return badge.classes().filter((name) => /^(dark:)?(bg|text)-/.test(name) && name !== 'text-xs')
    })
    expect(colours).toEqual([
      ['bg-green-50', 'text-green-700', 'dark:bg-green-500/[0.12]', 'dark:text-green-300'],
      ['bg-orange-50', 'text-orange-700', 'dark:bg-orange-400/[0.12]', 'dark:text-orange-300'],
      ['bg-sky-50', 'text-sky-700', 'dark:bg-sky-400/[0.12]', 'dark:text-sky-300'],
      ['bg-fuchsia-50', 'text-fuchsia-700', 'dark:bg-fuchsia-400/[0.12]', 'dark:text-fuchsia-300'],
      ['bg-zinc-100', 'text-zinc-700', 'dark:bg-zinc-400/[0.12]', 'dark:text-zinc-300']
    ])
  })

  it('shows a dash that still opens the dialog when there are no groups', async () => {
    const wrapper = mountCell([])
    expect(wrapper.get('button').text()).toBe('—')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('open')).toHaveLength(1)
  })
})
