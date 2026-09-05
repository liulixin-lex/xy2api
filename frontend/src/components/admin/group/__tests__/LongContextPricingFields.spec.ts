import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import LongContextPricingFields from '../LongContextPricingFields.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

function mountFields(overrides: Record<string, unknown> = {}) {
  return mount(LongContextPricingFields, {
    props: {
      scope: 'selected',
      models: [],
      candidates: ['gpt-5.6-sol', 'gpt-5.5', 'gpt-6-astra'],
      inputId: 'long-context-models',
      ...overrides
    },
    global: { stubs: { Icon: true } }
  })
}

describe('LongContextPricingFields', () => {
  it('switches between selected and all scopes', async () => {
    const wrapper = mountFields()
    const allButton = wrapper.get('button[aria-pressed="false"]')
    await allButton.trigger('click')
    expect(wrapper.emitted('update:scope')).toEqual([['all']])
  })

  it('searches candidates and adds a selected model', async () => {
    const wrapper = mountFields()
    const input = wrapper.get('input[role="combobox"]')
    await input.trigger('focus')
    await input.setValue('terra')
    expect(wrapper.text()).not.toContain('gpt-5.5')

    await input.setValue('5.6')
    const candidate = wrapper.findAll('button').find(button => button.text() === 'gpt-5.6-sol')
    expect(candidate).toBeDefined()
    await candidate!.trigger('mousedown')
    expect(wrapper.emitted('update:models')).toEqual([[['gpt-5.6-sol']]])
  })

  it('accepts a trailing-wildcard model pattern', async () => {
    const wrapper = mountFields()
    const input = wrapper.get('input[role="combobox"]')
    await input.setValue('gpt-5.6-*')
    await input.trigger('keydown.enter')
    expect(wrapper.emitted('update:models')).toEqual([[['gpt-5.6-*']]])
  })
})
