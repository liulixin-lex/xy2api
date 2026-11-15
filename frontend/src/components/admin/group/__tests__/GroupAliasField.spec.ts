import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'

import GroupAliasField from '../GroupAliasField.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const Host = defineComponent({
  components: { GroupAliasField },
  setup: () => ({ alias: ref(''), name: ref('GPT-不降智(稳定)') }),
  template: '<GroupAliasField ref="field" v-model="alias" :name="name" />'
})

describe('GroupAliasField', () => {
  it('previews the automatic text while empty, then the alias as typed', async () => {
    const wrapper = mount(Host)
    expect(wrapper.get('[data-test="group-alias-preview"]').text()).toBe('不降')
    expect(wrapper.text()).toContain('admin.groups.alias.auto')

    await wrapper.get('[data-test="group-alias-input"]').setValue('稳定')
    expect(wrapper.get('[data-test="group-alias-preview"]').text()).toBe('稳定')
    expect(wrapper.text()).not.toContain('admin.groups.alias.auto')
    expect(wrapper.find('[data-test="group-alias-error"]').exists()).toBe(false)
  })

  it('shows the length error inline while the alias is too long', async () => {
    const wrapper = mount(Host)
    const input = wrapper.get('[data-test="group-alias-input"]')
    await input.setValue('ABCD')
    expect(wrapper.get('[data-test="group-alias-error"]').text()).toBe('admin.groups.alias.tooLong')
    expect(input.attributes('aria-invalid')).toBe('true')
    await input.setValue('ABC')
    expect(wrapper.find('[data-test="group-alias-error"]').exists()).toBe(false)
  })

  it('shows the backend\'s rejection under the input until the alias changes', async () => {
    const wrapper = mount(Host)
    ;(wrapper.vm.$refs.field as { showServerError: () => void }).showServerError()
    await wrapper.vm.$nextTick()
    expect(wrapper.get('[data-test="group-alias-error"]').text()).toBe('admin.groups.alias.invalid')
    await wrapper.get('[data-test="group-alias-input"]').setValue('稳')
    expect(wrapper.find('[data-test="group-alias-error"]').exists()).toBe(false)
  })
})
