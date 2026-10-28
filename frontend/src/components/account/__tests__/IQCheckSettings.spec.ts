import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import IQCheckSettings from '../IQCheckSettings.vue'
import AccountTableFilters from '@/components/admin/account/AccountTableFilters.vue'
const { fetchModels, fetchTargets } = vi.hoisted(() => ({ fetchModels: vi.fn(), fetchTargets: vi.fn() }))
vi.mock('@/api/admin/accounts', () => ({ getIQCheckModels: fetchModels, getIQCheck: fetchTargets }))
vi.mock('@/api/admin/groupCategories', () => ({ default: { list: vi.fn(async () => []), save: vi.fn(), setGroupCategories: vi.fn() } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }))

const view = (model: string, patch: Record<string, unknown> = {}) => ({ model, enabled: true, reasoning_effort: 'low', interval_minutes: 5, timeout_seconds: 120, verdict: 'unknown', state: 'idle', gate_active: false, ...patch })
const rowsOf = (wrapper: ReturnType<typeof mount>) => wrapper.findAll('[data-testid="iq-target-row"]')
const effortSelect = (wrapper: ReturnType<typeof mount>, index = 0) => wrapper.findAllComponents(Select).filter(select => select.props('id')?.endsWith('-effort'))[index]
const lastPatch = (wrapper: ReturnType<typeof mount>) => wrapper.emitted('patch')?.at(-1)?.[0]
afterEach(() => { document.body.innerHTML = ''; vi.clearAllMocks() })

describe('IQ target settings', () => {
  it('loads targets and emits only changed fields, added targets and removals', async () => {
    fetchTargets.mockResolvedValue([view('custom-model'), view('gpt-6.1-sol', { enabled: false }), view('gpt-6-astra')])
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8 } })
    await flushPromises()
    expect(fetchTargets).toHaveBeenCalledWith(8)
    expect(rowsOf(wrapper).map(row => row.get('span[title]').text())).toEqual(['gpt-6-astra', 'gpt-6.1-sol', 'custom-model'])
    expect(lastPatch(wrapper)).toBeNull()
    await rowsOf(wrapper)[1].get('[role="switch"]').trigger('click')
    await rowsOf(wrapper)[0].get('input[type="number"]').setValue('15')
    await rowsOf(wrapper)[2].get('button[aria-label^="admin.accounts.iqRemoveTarget"]').trigger('click')
    expect(rowsOf(wrapper)[0].find('button[aria-label^="admin.accounts.iqRemoveTarget"]').exists()).toBe(false)
    await wrapper.get('[data-testid="iq-add-target"]').trigger('click')
    wrapper.findAllComponents(Select).find(select => select.props('id')?.endsWith('-model'))!.vm.$emit('update:modelValue', 'new-model')
    await flushPromises()
    expect(lastPatch(wrapper)).toEqual({
      targets: [{ model: 'gpt-6-astra', interval_minutes: 15 }, { model: 'gpt-6.1-sol', enabled: true }, { model: 'new-model', enabled: true, reasoning_effort: 'low', interval_minutes: 5, timeout_seconds: 120 }],
      remove: ['custom-model']
    })
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([true])
  })
  it('keeps the form savable when targets fail to load', async () => {
    fetchTargets.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8 } })
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('iqStatusFailed')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([true])
    expect(lastPatch(wrapper)).toBeNull()
  })
  it('bulk mode starts from built-in targets on keep and emits only the fields set', async () => {
    const wrapper = mount(IQCheckSettings, { props: { bulk: true } })
    await flushPromises()
    expect(fetchTargets).not.toHaveBeenCalled()
    expect(lastPatch(wrapper)).toBeNull()
    wrapper.findAllComponents(Select)[0].vm.$emit('update:modelValue', 'on')
    await rowsOf(wrapper)[1].get('input[type="number"]').setValue('30')
    await flushPromises()
    expect(lastPatch(wrapper)).toEqual({ targets: [{ model: 'gpt-6-astra', enabled: true }, { model: 'gpt-6.1-sol', interval_minutes: 30 }] })
  })
  it('blocks out-of-range values, duplicate and invalid models', async () => {
    fetchTargets.mockResolvedValue([view('gpt-6-astra')])
    const wrapper = mount(IQCheckSettings, { props: { accountId: 8 } })
    await flushPromises()
    await rowsOf(wrapper)[0].get('input[type="number"]').setValue('0')
    expect(wrapper.get('[role="alert"]').text()).toContain('iqIntervalInvalid')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([false])
    await rowsOf(wrapper)[0].get('input[type="number"]').setValue('5')
    await wrapper.get('[data-testid="iq-add-target"]').trigger('click')
    expect(wrapper.get('[role="alert"]').text()).toContain('iqModelInvalid')
    wrapper.findAllComponents(Select).find(select => select.props('id')?.endsWith('-model'))!.vm.$emit('update:modelValue', 'gpt-6-astra')
    await flushPromises()
    expect(wrapper.text()).toContain('iqModelDuplicate')
    expect(lastPatch(wrapper)).toBeNull()
  })
  it('emits the IQ filter without discarding other active filters', async () => {
    const wrapper = mount(AccountTableFilters, { props: { searchQuery: '', filters: { platform: 'openai', status: 'active', iq_status: '' } }, global: { stubs: { Select: { props: ['options', 'modelValue'], template: '<button @click="$emit(\'update:model-value\', \'degraded\')">{{ JSON.stringify(options) }}</button>' }, SearchInput: true } } })
    const iqFilter = wrapper.findAll('button').find(button => button.text().includes('allIQStatuses'))!
    expect(iqFilter.text()).toContain('smart'); expect(iqFilter.text()).toContain('unknown')
    await iqFilter.trigger('click')
    expect(wrapper.emitted('update:filters')?.[0]).toEqual([{ platform: 'openai', status: 'active', iq_status: 'degraded' }])
  })})

describe('IQ catalog and effort capabilities', () => {
  const value = view('custom')
  beforeEach(() => { fetchModels.mockReset(); fetchTargets.mockResolvedValue([value]) })
  const mountLoaded = async (props: Record<string, unknown> = {}) => { const wrapper = mount(IQCheckSettings, { props: { accountId: 8, ...props } }); await flushPromises(); return wrapper }

  it('fetches actual models, validates declared effort and permits upstream default', async () => {
    fetchModels.mockResolvedValue({ models: [{ id: 'custom', display_name: 'Custom', supported_reasoning_levels: ['ultra'], capability_sources: { supported_reasoning_levels: 'upstream' } }], fetched_at: null, from_cache: false, stale: false })
    const wrapper = await mountLoaded()
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click'); await flushPromises()
    expect(fetchModels).toHaveBeenLastCalledWith(8, true, expect.any(AbortSignal))
    expect(wrapper.get('[role="alert"]').text()).toContain('iqEffortUnsupported')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([false])
    effortSelect(wrapper).vm.$emit('update:modelValue', 'upstream_default'); await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(lastPatch(wrapper)).toEqual({ targets: [{ model: 'custom', reasoning_effort: 'upstream_default' }] })
    await wrapper.setProps({ discoveryDisabled: true })
    expect(wrapper.get('[data-testid="iq-sync-models"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('iqSaveCredentials')
  })
  it('discards catalog results after switching accounts and reports failures', async () => {
    let resolve!: (value: unknown) => void
    fetchModels.mockImplementationOnce(() => new Promise(r => { resolve = r }))
    const wrapper = await mountLoaded()
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click')
    await wrapper.setProps({ accountId: 9 }); await flushPromises()
    resolve({ models: [{ id: 'old-account', capability_sources: {} }], fetched_at: null }); await flushPromises()
    expect(wrapper.text()).not.toContain('iqModelsReady')
    fetchModels.mockRejectedValueOnce(new Error('offline'))
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click'); await flushPromises()
    expect(wrapper.text()).toContain('iqModelsFailed')
  })
  it.each([
    ['upstream', 'iqCapabilitiesUpstream'],
    ['reference', 'iqCapabilitiesReference'],
    [undefined, 'iqCapabilitiesUnknown']
  ])('labels %s effort capabilities', async (source, message) => {
    fetchModels.mockResolvedValueOnce({ models: [{ id: 'custom', source: 'upstream', supported_reasoning_levels: ['low'], capability_sources: source ? { supported_reasoning_levels: source } : {} }], fetched_at: null, from_cache: false, stale: false })
    const wrapper = await mountLoaded()
    await wrapper.get('[data-testid="iq-sync-models"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[id$="-effort-source"]').text()).toBe('admin.accounts.' + message)
    expect(lastPatch(wrapper)).toBeNull()
  })
  it('offers GPT-6.1 Sol reference levels and marks unsupported saved levels invalid', async () => {
    fetchTargets.mockResolvedValue([view('gpt-6.1-sol', { reasoning_effort: 'minimal' })])
    const wrapper = await mountLoaded()
    expect(effortSelect(wrapper).props('options').map((option: { value: string }) => option.value)).toEqual(['upstream_default', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra', 'minimal'])
    expect(wrapper.get('[id$="-effort-source"]').text()).toBe('admin.accounts.iqCapabilitiesReference')
    expect(wrapper.emitted('validity')?.at(-1)).toEqual([false])
  })
})
