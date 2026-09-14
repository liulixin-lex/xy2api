import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, describe, it, expect, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import IQCheckSettings from '../IQCheckSettings.vue'
import AccountTableFilters from '@/components/admin/account/AccountTableFilters.vue'
const fetchModels = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/accounts', () => ({ getIQCheckModels: fetchModels }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }))
afterEach(() => { document.body.innerHTML = ''; vi.clearAllMocks() })
describe('IQ check settings', () => {
  it('changes only a selected schedule field and keeps model settings', async () => {
    const value = { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'high', daily_request_limit: 96 }
    const wrapper = mount(IQCheckSettings, { props: { modelValue: value, fields: ['scheduling_mode'] } })
    const schedule = wrapper.findAllComponents(Select).find(s => s.props('id')?.endsWith('-schedule'))!
    schedule.vm.$emit('update:modelValue', 'adaptive')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ ...value, scheduling_mode: 'adaptive' }])
    expect(wrapper.findAll('input[type="number"]').every(i => i.attributes('disabled') !== undefined)).toBe(true)
  })
  it('fetches actual models, validates declared effort and permits upstream default', async () => {
    fetchModels.mockResolvedValue({ models: [{ id: 'custom', display_name: 'Custom', supported_reasoning_levels: ['ultra'], capability_sources: { supported_reasoning_levels: 'upstream' } }], fetched_at: null, from_cache: false, stale: false })
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'low' } } })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click'); await flushPromises()
    expect(fetchModels).toHaveBeenLastCalledWith(8, true, expect.any(AbortSignal))
    expect(wrapper.get('[role="alert"]').text()).toContain('iqEffortUnsupported')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([false])
    await wrapper.setProps({ modelValue: { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'upstream_default' } })
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([true])
    await wrapper.setProps({ discoveryDisabled: true })
    expect(wrapper.get('[data-testid="iq-sync-models"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('iqSaveCredentials')
  })
  it('discards results after switching accounts, and keeps custom input usable after failure', async () => {
    let resolve!: (value: unknown) => void
    fetchModels.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: { enabled: true, interval_minutes: 15, model: 'mine', reasoning_effort: 'ultra' } } })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await wrapper.setProps({ accountId: 9 })
    resolve({ models: [{ id: 'old-account' }], fetched_at: null }); await flushPromises()
    expect(wrapper.findAllComponents(Select)[0].props('options').some(o => o.value === 'old-account')).toBe(false)
    fetchModels.mockRejectedValueOnce(new Error('offline'))
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('iqModelsFailed')
    const model = wrapper.findAllComponents(Select).find(s => s.props('id')?.endsWith('-model'))!
    model.vm.$emit('update:modelValue', 'another-model')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([{ enabled: true, interval_minutes: 15, model: 'another-model', reasoning_effort: 'ultra' }])
  })
  it('disables fields excluded from a partial bulk edit without resetting their values', () => {
    const wrapper = mount(IQCheckSettings, { props: { fields: ['model'], modelValue: { enabled: true, interval_minutes: 60, model: 'custom', reasoning_effort: 'high' } } })
    expect(wrapper.get('[role="switch"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[type="number"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('button[id$="-model"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
  it('emits the blue switch setting and an independent bounded interval', async () => {
    const wrapper = mount(IQCheckSettings, { props: { modelValue: { enabled: false, interval_minutes: 15 } } })
    const input = wrapper.get('input[type="number"]')
    expect(input.attributes('min')).toBe('1'); expect(input.attributes('max')).toBe('1440')
    expect((input.element as HTMLInputElement).value).toBe('15')
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ enabled: true, interval_minutes: 15 }])
    await input.setValue('30')
    expect(wrapper.emitted('update:modelValue')?.[1]).toEqual([{ enabled: false, interval_minutes: 30 }])
  })
  it('uses native dropdown search, custom selection, Escape and outside-click behavior', async () => {
    const wrapper = mount(IQCheckSettings, { attachTo: document.body, props: { modelValue: { enabled: true, interval_minutes: 15, model: 'custom' } } })
    const trigger = wrapper.get('button[id$="-model"]')
    await trigger.trigger('click')
    await flushPromises()
    const search = document.querySelector<HTMLInputElement>('.select-search-input')!
    search.value = 'my-new-model'
    search.dispatchEvent(new Event('input', { bubbles: true }))
    await flushPromises()
    const option = document.querySelector<HTMLElement>('[role="option"]')!
    expect(option.textContent).toContain('my-new-model')
    option.click()
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toMatchObject({ model: 'my-new-model' })
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger.element)
    await trigger.trigger('click')
    await flushPromises()
    document.querySelector('.select-search-input')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    await flushPromises()
    document.body.click()
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })
  it('keeps selection through a failed refresh and merges repeated clicks', async () => {
    const value = { enabled: true, interval_minutes: 15, model: 'mine', reasoning_effort: 'high' }
    let reject!: (error: Error) => void
    fetchModels.mockImplementationOnce(() => new Promise((_, r) => { reject = r }))
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: value } })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    expect(fetchModels).toHaveBeenCalledTimes(1)
    reject(new Error('offline')); await flushPromises()
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('button[id$="-model"]').text()).toContain('mine')
    wrapper.unmount()
  })
  it('emits the IQ filter without discarding other active filters', async () => {
    const wrapper = mount(AccountTableFilters, { props: { searchQuery: '', filters: { platform: 'openai', status: 'active', iq_status: '' } }, global: { stubs: { Select: { props: ['options', 'modelValue'], template: '<button @click="$emit(\'update:model-value\', \'degraded\')">{{ JSON.stringify(options) }}</button>' }, SearchInput: true } } })
    const iqFilter = wrapper.findAll('button').find(button => button.text().includes('allIQStatuses'))!
    expect(iqFilter.text()).toContain('smart'); expect(iqFilter.text()).toContain('unknown')
    await iqFilter.trigger('click')
    expect(wrapper.emitted('update:filters')?.[0]).toEqual([{ platform: 'openai', status: 'active', iq_status: 'degraded' }])
  })
})
