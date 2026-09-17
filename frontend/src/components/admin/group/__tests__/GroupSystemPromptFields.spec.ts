import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import GroupSystemPromptFields from '../GroupSystemPromptFields.vue'
import Select from '@/components/common/Select.vue'
import { cloneGroupSystemPromptConfig } from '@/utils/groupSystemPrompt'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function setup() {
  return mount(GroupSystemPromptFields, {
    props: { modelValue: cloneGroupSystemPromptConfig(), idPrefix: 'test', candidates: ['alias', 'second'] },
    global: { stubs: { Select: { props: ['modelValue', 'options'], emits: ['update:modelValue'], template: '<div />' }, Icon: true } }
  })
}

describe('GroupSystemPromptFields', () => {
  it('requires models for a nonempty selected prompt and preserves typed content', async () => {
    const wrapper = setup()
    await wrapper.get('#test-prompt').setValue('common')
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.vm.validate()).toBe(false)
    await wrapper.vm.$nextTick()
    expect(wrapper.get('[role="alert"]').text()).toContain('selectModelsError')
    wrapper.findComponent(Select).vm.$emit('update:modelValue', ' alias ')
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.validate()).toBe(true)
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ prompt: 'common', scope: 'selected', models: ['alias'], model_prompts: {} })
    expect((wrapper.get('#test-prompt').element as HTMLTextAreaElement).value).toBe('common')
  })

  it('rejects duplicate trimmed aliases and saves independent prompts outside the common list', async () => {
    const wrapper = setup()
    const add = wrapper.findAll('button').find(button => button.text().includes('addModel'))!
    await add.trigger('click')
    await add.trigger('click')
    const selects = wrapper.findAllComponents(Select)
    selects[0].vm.$emit('update:modelValue', 'alias')
    selects[1].vm.$emit('update:modelValue', ' alias ')
    await wrapper.vm.$nextTick()
    expect(wrapper.vm.validate()).toBe(false)
    await wrapper.vm.$nextTick()
    expect(wrapper.get('[role="alert"]').text()).toContain('duplicateModel')
    selects[1].vm.$emit('update:modelValue', 'second')
    await wrapper.vm.$nextTick()
    await wrapper.findAll('textarea')[1].setValue('specific')
    expect(wrapper.vm.validate()).toBe(true)
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toMatchObject({ model_prompts: { alias: 'specific' } })
  })

  it('resets drafts when switching groups and accepts a fully cleared configuration', async () => {
    const wrapper = setup()
    await wrapper.get('#test-prompt').setValue('unsaved')
    await wrapper.setProps({ modelValue: cloneGroupSystemPromptConfig({ prompt: 'other group', scope: 'all', models: [], model_prompts: { second: 'second policy' } }) })
    expect(wrapper.findAll('textarea')).toHaveLength(2)
    expect((wrapper.get('#test-prompt').element as HTMLTextAreaElement).value).toBe('other group')
    await wrapper.setProps({ modelValue: cloneGroupSystemPromptConfig() })
    expect(wrapper.findAll('textarea')).toHaveLength(1)
    expect(wrapper.vm.validate()).toBe(true)
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(cloneGroupSystemPromptConfig())
  })
})
