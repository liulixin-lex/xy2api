import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ClaudeCacheFallbackSettings from '../ClaudeCacheFallbackSettings.vue'

const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), groups: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { settings: { getSettings: mocks.get, updateSettings: mocks.save }, groups: { getAllIncludingInactive: mocks.groups } } }))
function mountSettings() {
  return mount(ClaudeCacheFallbackSettings, { global: { stubs: {
    Toggle: { props: ['modelValue', 'disabled'], emits: ['update:modelValue'], template: '<button type="button" data-toggle :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)">Toggle</button>' },
    Select: { props: ['options', 'disabled'], emits: ['update:modelValue'], template: '<select data-groups :disabled="disabled" @change="$emit(\'update:modelValue\', Number($event.target.value))"><option value="">Select</option><option v-for="o in options" :key="o.value" :value="o.value">{{o.label}}</option></select>' },
    Icon: true
  } } })
}
function button(w: ReturnType<typeof mountSettings>, key: string) { return w.findAll('button').find(b => b.text() === `admin.settings.claudeCacheFallback.${key}`)! }
async function save(w: ReturnType<typeof mountSettings>) { await button(w, 'save').trigger('click'); await flushPromises() }
describe('Claude cache group settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.groups.mockResolvedValue([{ id: 7, name: 'Claude', platform: 'anthropic' }, { id: 8, name: 'Mixed', platform: 'composite' }])
    mocks.get.mockResolvedValue({ claude_cache_fallback_policy: { enabled: false, group_ids: [7] } })
    mocks.save.mockImplementation(async payload => payload)
  })
  it('saves only the switch and selected groups, including composite groups', async () => {
    const w = mountSettings(); await flushPromises()
    expect(button(w, 'save').attributes('disabled')).toBeDefined()
    expect(w.find('textarea').exists()).toBe(false)
    expect(w.findAll('[data-groups] option').map(o => o.text())).toEqual(['Select', 'Mixed'])
    await w.get('[data-toggle]').trigger('click'); await w.get('[data-groups]').setValue('8'); await save(w)
    expect(mocks.save).toHaveBeenCalledWith({ claude_cache_fallback_policy: { enabled: true, group_ids: [7, 8] } })
    expect(button(w, 'save').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('claudeCacheFallback.saved')
  })
  it('requires a group only when enabled and can disable with an empty selection', async () => {
    mocks.get.mockResolvedValue({ claude_cache_fallback_policy: { enabled: false, group_ids: [] } })
    const w = mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click'); await save(w)
    expect(mocks.save).not.toHaveBeenCalled(); expect(w.text()).toContain('claudeCacheFallback.invalid')
    await w.get('[data-groups]').setValue('7'); await save(w)
    await w.get('[data-toggle]').trigger('click'); await w.get('li button').trigger('click'); await save(w)
    expect(mocks.save).toHaveBeenLastCalledWith({ claude_cache_fallback_policy: { enabled: false, group_ids: [] } })
  })
  it('retains edits after save failure and hides the saved status after further edits', async () => {
    mocks.save.mockRejectedValueOnce(new Error('unavailable'))
    const w = mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click'); await save(w)
    expect(w.text()).toContain('claudeCacheFallback.saveFailed'); expect(button(w, 'save').attributes('disabled')).toBeUndefined()
    await save(w); expect(mocks.save.mock.calls[1][0].claude_cache_fallback_policy.enabled).toBe(true)
    await w.get('[data-toggle]').trigger('click'); expect(w.text()).not.toContain('claudeCacheFallback.saved')
  })
  it.each([{}, { claude_cache_fallback_policy: { enabled: true, rules: [] } }, { claude_cache_fallback_policy: { enabled: false, group_ids: [7] } }])('rejects ignored or incompatible save responses: %j', async result => {
    mocks.save.mockResolvedValue(result)
    const w = mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click'); await save(w)
    expect(w.text()).toContain('claudeCacheFallback.saveFailed'); expect(w.text()).not.toContain('claudeCacheFallback.saved')
  })
  it('does not expose save for an old backend and recovers by reload', async () => {
    mocks.get.mockResolvedValueOnce({ claude_cache_fallback_policy: { enabled: true, rules: [] } })
    const w = mountSettings(); await flushPromises(); expect(button(w, 'save')).toBeUndefined()
    await button(w, 'reload').trigger('click'); await flushPromises(); expect(button(w, 'save')).toBeDefined()
  })
  it('can disable a saved group even when the group directory is unavailable', async () => {
    mocks.get.mockResolvedValue({ claude_cache_fallback_policy: { enabled: true, group_ids: [99] } })
    mocks.groups.mockRejectedValue(new Error('unavailable'))
    const w = mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click'); await save(w)
    expect(mocks.save).toHaveBeenCalledWith({ claude_cache_fallback_policy: { enabled: false, group_ids: [99] } })
  })
  it('preserves edited scope when retrying the directory', async () => {
    mocks.groups.mockRejectedValueOnce(new Error('unavailable'))
    const w = mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click')
    await button(w, 'reload').trigger('click'); await flushPromises(); await save(w)
    expect(mocks.save).toHaveBeenCalledWith({ claude_cache_fallback_policy: { enabled: true, group_ids: [7] } })
  })
})
