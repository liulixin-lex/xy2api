import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupOptionItem from '../GroupOptionItem.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

describe('GroupOptionItem description layout', () => {
  it('applies multiline and overflow-safe text styles', () => {
    const description = 'First section\nvery-long-unbroken-description-value-that-must-not-overflow'
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Example group',
        platform: 'openai',
        description,
      },
      global: {
        stubs: {
          GroupBadge: true,
        },
      },
    })

    const descriptionElement = wrapper
      .findAll('span')
      .find((element) => element.text() === description)

    expect(descriptionElement).toBeDefined()
    expect(descriptionElement?.classes()).toContain('whitespace-pre-line')
    expect(descriptionElement?.classes()).toContain('[overflow-wrap:anywhere]')
    expect(descriptionElement?.classes()).toContain('line-clamp-3')
    expect(wrapper.find('[title]').attributes('title')).toBe(description)
  })
})

describe('GroupOptionItem exclusive tag', () => {
  const mountItem = (props: Record<string, unknown> = {}) => mount(GroupOptionItem, {
    props: { name: 'Example group', platform: 'openai', ...props },
    global: { stubs: { GroupBadge: true, Icon: true } },
  })

  it('is hidden by default', () => {
    expect(mountItem().find('[data-test="exclusive-group-tag"]').exists()).toBe(false)
  })

  it('shows the user-facing exclusive label when requested', () => {
    const tag = mountItem({ exclusive: true }).get('[data-test="exclusive-group-tag"]')
    expect(tag.text()).toBe('availableChannels.exclusive')
    expect(tag.attributes('title')).toBe('availableChannels.exclusiveTooltip')
  })
})

describe('GroupOptionItem description popover', () => {
  const description = 'A long group description'
  const mountItem = (props: Record<string, unknown> = {}) => mount(GroupOptionItem, {
    props: { name: 'Example group', platform: 'openai', description, ...props },
    global: { stubs: { GroupBadge: true } },
  })

  it('keeps the native title and the popover off by default', () => {
    const wrapper = mountItem()
    expect(wrapper.find('[title]').attributes('title')).toBe(description)
    expect(wrapper.getComponent({ name: 'TruncatedTextPopover' }).props('enabled')).toBe(false)
  })

  it('swaps the native title for the hover popover when requested', () => {
    const wrapper = mountItem({ descriptionPopover: true })
    expect(wrapper.find('[title]').exists()).toBe(false)
    const text = wrapper.getComponent({ name: 'TruncatedTextPopover' })
    expect(text.props('enabled')).toBe(true)
    expect(text.classes()).toContain('line-clamp-3')
  })
})
