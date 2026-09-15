import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import IQCheckSettings from '../IQCheckSettings.vue'
import AccountTableFilters from '@/components/admin/account/AccountTableFilters.vue'
const fetchModels = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/accounts', () => ({ getIQCheckModels: fetchModels }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }))
afterEach(() => { document.body.innerHTML = ''; vi.clearAllMocks() })
describe('IQ check settings', () => {
  it('edits only the selected timeout and omits retired settings', async () => {
    const value = { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'high', timeout_seconds: 120 }
    const wrapper = mount(IQCheckSettings, { props: { modelValue: value, fields: ['timeout_seconds'] } })
    await wrapper.get('input[id$="-timeout_seconds"]').setValue('180')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{ ...value, timeout_seconds: 180 }])
    expect(wrapper.get('input[id$="-interval"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('input[id$="-daily_request_limit"]').exists()).toBe(false)
    expect(wrapper.find('button[id$="-schedule"]').exists()).toBe(false)
    expect(wrapper.find('input[id$="-group"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('iqCompatDescription')
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

describe('IQ validity and catalog feedback', () => {
  let wrapper: ReturnType<typeof mount<typeof IQCheckSettings>>
  const value = { enabled: true, interval_minutes: 15, model: 'custom', reasoning_effort: 'low' }

  beforeEach(() => fetchModels.mockReset())
  afterEach(() => wrapper?.unmount())

  it('re-emits invalidity when restored settings have the same validation error', async () => {
    const invalid = { ...value, model: 'bad model' }
    wrapper = mount(IQCheckSettings, { props: { modelValue: invalid } })
    expect(wrapper.emitted('validity')).toEqual([[false]])
    await wrapper.setProps({ modelValue: { ...invalid } })
    expect(wrapper.emitted('validity')).toEqual([[false], [false]])
  })

  it.each([
    ['upstream', 'iqCapabilitiesUpstream'],
    ['reference', 'iqCapabilitiesReference'],
    [undefined, 'iqCapabilitiesUnknown']
  ])('separates upstream model source from %s effort capabilities', async (source, message) => {
    fetchModels.mockResolvedValueOnce({
      models: [{ id: 'custom', display_name: 'Custom', source: 'upstream', supported_reasoning_levels: ['low'], capability_sources: { ...(source ? { supported_reasoning_levels: source } : {}), default_reasoning_level: 'upstream' } }],
      fetched_at: null, from_cache: false, stale: false
    })
    wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: value } })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[id$="-model-source"]').text()).toBe('admin.accounts.iqModelsUpstream')
    expect(wrapper.get('[id$="-effort-source"]').text()).toBe('admin.accounts.' + message)
    expect(wrapper.get('button[id$="-model"]').attributes('aria-describedby')).toBe(wrapper.get('[id$="-model-source"]').attributes('id'))
    expect(wrapper.get('button[id$="-effort"]').attributes('aria-describedby')).toBe(wrapper.get('[id$="-effort-source"]').attributes('id'))
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('uses the reasoning capability source when the model declares no reasoning support', async () => {
    fetchModels.mockResolvedValueOnce({
      models: [{ id: 'custom', display_name: 'Custom', source: 'upstream', reasoning: false, capability_sources: { reasoning: 'upstream', supported_reasoning_levels: 'reference' } }],
      fetched_at: null, from_cache: false, stale: false
    })
    wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: { ...value, reasoning_effort: 'none' } } })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[id$="-effort-source"]').text()).toBe('admin.accounts.iqCapabilitiesUpstream')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([true])
  })

  it('keeps ID-only capabilities and unsynced custom model sources unknown', async () => {
    wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: { ...value, reasoning_effort: 'custom-effort' } } })
    expect(wrapper.get('[id$="-model-source"]').text()).toBe('admin.accounts.iqModelSourceUnknown')
    expect(wrapper.get('[id$="-effort-source"]').text()).toBe('admin.accounts.iqCapabilitiesUnknown')
    fetchModels.mockResolvedValueOnce({
      models: [{ id: 'custom', display_name: 'Custom', source: 'upstream', capability_sources: {} }],
      fetched_at: null, from_cache: false, stale: false
    })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[id$="-model-source"]').text()).toBe('admin.accounts.iqModelsUpstream')
    expect(wrapper.get('[id$="-effort-source"]').text()).toBe('admin.accounts.iqCapabilitiesUnknown')
    expect(wrapper.findAllComponents(Select).find(select => select.props('id')?.endsWith('-effort'))!.props('creatable')).toBe(true)
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([true])
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it.each(['catalog', 'request'])('shows stale, cached and error feedback together after a %s failure without changing selections', async failure => {
    fetchModels.mockResolvedValueOnce({
      models: [{ id: 'custom', display_name: 'Custom', source: 'upstream', capability_sources: {} }],
      fetched_at: null, from_cache: true, stale: true,
      ...(failure === 'catalog' ? { error: 'model_discovery_failed' } : {})
    })
    wrapper = mount(IQCheckSettings, { props: { accountId: 8, modelValue: value } })
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await flushPromises()
    if (failure === 'request') {
      fetchModels.mockRejectedValueOnce(new Error('offline'))
      await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
      await flushPromises()
    }
    expect(wrapper.text()).toContain('admin.accounts.iqModelsFailed')
    expect(wrapper.text()).toContain('admin.accounts.iqModelsStale')
    expect(wrapper.text()).toContain('admin.accounts.iqModelsCached')
    expect(wrapper.get('button[id$="-model"]').text()).toContain('custom')
    expect(wrapper.get('button[id$="-effort"]').text()).toContain('low')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
})
