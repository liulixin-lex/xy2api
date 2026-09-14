import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import IQCheckSettings from '../IQCheckSettings.vue'
import AccountTableFilters from '@/components/admin/account/AccountTableFilters.vue'
const fetchModels = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/accounts', () => ({ getIQCheckModels: fetchModels }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('IQ check settings', () => {
  it('changes only a selected schedule field and keeps model settings', async () => {
    const value = { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'high', daily_request_limit: 96 }
    const wrapper = mount(IQCheckSettings, { props: { modelValue: value, fields: ['scheduling_mode'] } })
    const schedule = wrapper.findAll('select').find(s => s.find('option[value="adaptive"]').exists())!
    await schedule.setValue('adaptive')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ ...value, scheduling_mode: 'adaptive' }])
    expect(wrapper.findAll('input[type="number"]').every(i => i.attributes('disabled') !== undefined)).toBe(true)
  })
  it('fetches actual models, validates declared effort and permits upstream default', async () => {
    fetchModels.mockResolvedValue({ models: [{ id: 'custom', display_name: 'Custom', supported_reasoning_levels: ['ultra'], capability_sources: { supported_reasoning_levels: 'upstream' } }], fetched_at: null, from_cache: false, stale: false })
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'low' } } })
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(fetchModels).toHaveBeenLastCalledWith(8, false, expect.any(AbortSignal))
    expect(wrapper.get('[role="alert"]').text()).toContain('iqEffortUnsupported')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([false])
    await wrapper.setProps({ modelValue: { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'upstream_default' } })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([true])
    await wrapper.setProps({ discoveryDisabled: true })
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('iqSaveCredentials')
  })
  it('discards results after switching accounts, and keeps custom input usable after failure', async () => {
    let resolve!: (value: unknown) => void
    fetchModels.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: { enabled: true, interval_minutes: 15, model: 'mine', reasoning_effort: 'ultra' } } })
    await wrapper.get('button').trigger('click')
    await wrapper.setProps({ accountId: 9 })
    resolve({ models: [{ id: 'old-account' }], fetched_at: null }); await flushPromises()
    expect(wrapper.find('option[value="old-account"]').exists()).toBe(false)
    fetchModels.mockRejectedValueOnce(new Error('offline'))
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('iqModelsFailed')
    const model = wrapper.get('input[id$="-model"]')
    await model.setValue('another-model')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([{ enabled: true, interval_minutes: 15, model: 'another-model', reasoning_effort: 'ultra' }])
  })
  it('disables fields excluded from a partial bulk edit without resetting their values', () => {
    const wrapper = mount(IQCheckSettings, { props: { fields: ['model'], modelValue: { enabled: true, interval_minutes: 60, model: 'custom', reasoning_effort: 'high' } } })
    expect(wrapper.get('input[type="checkbox"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[id$="-model"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
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
