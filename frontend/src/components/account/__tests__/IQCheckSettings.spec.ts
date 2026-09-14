import { mount } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import IQCheckSettings from '../IQCheckSettings.vue'
import AccountTableFilters from '@/components/admin/account/AccountTableFilters.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('IQ check settings', () => {
  it('emits the blue switch setting and an independent bounded interval', async () => {
    const wrapper = mount(IQCheckSettings, { props: { modelValue: { enabled: false, interval_minutes: 15 } } })
    const input = wrapper.get('input[type="number"]')
    expect(input.attributes('min')).toBe('1'); expect(input.attributes('max')).toBe('1440')
    expect((input.element as HTMLInputElement).value).toBe('15')
    await wrapper.get('input[type="checkbox"]').setValue(true)
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ enabled: true, interval_minutes: 15 }])
    await input.setValue('30')
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([{ enabled: false, interval_minutes: 30 }])
  })
  it('emits the IQ filter without discarding other active filters', async () => {
    const wrapper = mount(AccountTableFilters, { props: { searchQuery: '', filters: { platform: 'openai', status: 'active', iq_status: '' } }, global: { stubs: { Select: { props: ['options', 'modelValue'], template: '<button @click="$emit(\'update:model-value\', \'degraded\')">{{ JSON.stringify(options) }}</button>' }, SearchInput: true } } })
    const iqFilter = wrapper.findAll('button').find(button => button.text().includes('allIQStatuses'))!
    expect(iqFilter.text()).toContain('smart'); expect(iqFilter.text()).toContain('unknown')
    await iqFilter.trigger('click')
    expect(wrapper.emitted('update:filters')?.[0]).toEqual([{ platform: 'openai', status: 'active', iq_status: 'degraded' }])
  })
})
