// Mobile list plan, step 2 (groups): below 1024px a group row shows only "⋯"; its menu holds the row's actions in the
// wide row's order, each with its own permission and disabled state.
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupActionMenu from '../GroupActionMenu.vue'
import { groupRowActions, hasRowActions } from '../groupRowActions'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const full = { simpleMode: false, manage: true, adjustBalance: true }

describe('groupRowActions', () => {
  it('offers the board for OpenAI and composite routes for composite groups', () => {
    expect(groupRowActions({ platform: 'openai' }, full)).toEqual({ board: true, edit: true, duplicate: true, composite: false, rate: true, rpm: true, delete: true })
    expect(groupRowActions({ platform: 'composite' }, full)).toMatchObject({ board: false, composite: true })
  })
  it('keeps user rates for the balance permission and leaves only the basics in simple mode', () => {
    expect(groupRowActions({ platform: 'anthropic' }, { ...full, adjustBalance: false }).rate).toBe(false)
    expect(groupRowActions({ platform: 'openai' }, { ...full, simpleMode: true })).toEqual({ board: false, edit: true, duplicate: false, composite: false, rate: false, rpm: false, delete: true })
  })
  it('gives a viewer without the groups permission only the board', () => {
    const viewer = groupRowActions({ platform: 'openai' }, { ...full, manage: false })
    expect(Object.entries(viewer).filter(([, on]) => on).map(([key]) => key)).toEqual(['board'])
    expect(hasRowActions(groupRowActions({ platform: 'anthropic' }, { ...full, manage: false }))).toBe(false)
  })
})

describe('GroupActionMenu', () => {
  const mountMenu = (props: Record<string, unknown> = {}) => mount(GroupActionMenu, {
    props: { show: true, group: { id: 7, name: 'g' }, anchorRect: new DOMRect(300, 100, 40, 40), actions: groupRowActions({ platform: 'openai' }, full), ...props },
    global: { stubs: { Teleport: true, Icon: true } },
  })

  it('lists the allowed actions in the wide row\'s order and reports the chosen one', async () => {
    const wrapper = mountMenu()
    expect(wrapper.findAll('[role="menuitem"]').map((el) => el.attributes('data-test')))
      .toEqual(['menu-board', 'menu-edit', 'menu-duplicate', 'menu-rate-multipliers', 'menu-rpm-overrides', 'menu-delete'])
    await wrapper.get('[data-test="menu-rpm-overrides"]').trigger('click')
    expect(wrapper.emitted('action')).toEqual([['rpm']])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('disables copying while the group is being copied', () => {
    const item = mountMenu({ duplicating: true }).get('[data-test="menu-duplicate"]')
    expect(item.attributes('disabled')).toBeDefined()
    expect(item.text()).toBe('admin.groups.duplicating')
  })
})
