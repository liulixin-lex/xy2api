import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ClaudeCacheFallbackSettings from '../ClaudeCacheFallbackSettings.vue'

const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn(), groups: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { settings: { getSettings: mocks.get, updateSettings: mocks.save }, groups: { getAllIncludingInactive: mocks.groups } } }))
const rule = { id: 'pilot', group_id: 7, account_id: 11, api_key_ids: [9], base_url: 'https://example.com/base', models: ['claude-sonnet-4-5'] }
function mountSettings() { return mount(ClaudeCacheFallbackSettings, { global: { stubs: { Toggle: { props: ['modelValue'], emits: ['update:modelValue'], template: '<button type="button" data-toggle @click="$emit(\'update:modelValue\', !modelValue)">Toggle</button>' }, ClaudeCacheFallbackRuleEditor: true } } }) }
function saveButton(wrapper: ReturnType<typeof mountSettings>) { return wrapper.findAll('button').find(b => b.text() === 'admin.settings.claudeCacheFallback.save')! }
describe('Claude cache fallback settings', () => {
 beforeEach(() => { vi.clearAllMocks(); mocks.groups.mockResolvedValue([]); mocks.get.mockResolvedValue({ claude_cache_fallback_policy: { enabled: false, rules: [structuredClone(rule)] } }); mocks.save.mockImplementation(async payload => payload) })
 it('saves only this policy and preserves exact account, URL, model and key scope', async () => {
  const w=mountSettings(); await flushPromises(); expect(saveButton(w).attributes('disabled')).toBeDefined()
  await w.get('[data-toggle]').trigger('click'); await saveButton(w).trigger('click'); await flushPromises()
  expect(mocks.save).toHaveBeenCalledWith({ claude_cache_fallback_policy: { enabled: true, rules: [rule] } })
  expect(saveButton(w).attributes('disabled')).toBeDefined(); expect(w.text()).toContain('claudeCacheFallback.saved')
 })
 it('retains a failed save for retry and can disable without network probes', async () => {
  mocks.get.mockResolvedValue({ claude_cache_fallback_policy: { enabled: true, rules: [rule] } }); mocks.save.mockRejectedValueOnce(new Error('unavailable'))
  const w=mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click'); await saveButton(w).trigger('click'); await flushPromises()
  expect(w.text()).toContain('claudeCacheFallback.saveFailed'); expect(saveButton(w).attributes('disabled')).toBeUndefined()
  await saveButton(w).trigger('click'); await flushPromises(); expect(mocks.save.mock.calls[1][0].claude_cache_fallback_policy.enabled).toBe(false)
 })
 it('does not claim success when an older backend ignores the policy', async () => {
  mocks.save.mockResolvedValue({}); const w=mountSettings(); await flushPromises(); await w.get('[data-toggle]').trigger('click'); await saveButton(w).trigger('click'); await flushPromises()
  expect(w.text()).toContain('claudeCacheFallback.saveFailed'); expect(w.text()).not.toContain('claudeCacheFallback.saved'); expect(saveButton(w).attributes('disabled')).toBeUndefined()
 })
 it('does not expose a save action on read failure and recovers by explicit reload', async () => {
  mocks.get.mockRejectedValueOnce(new Error('unavailable')); const w=mountSettings(); await flushPromises()
  expect(saveButton(w)).toBeUndefined(); expect(mocks.save).not.toHaveBeenCalled()
  await w.findAll('button').find(b=>b.text().includes('reload'))!.trigger('click'); await flushPromises(); expect(saveButton(w)).toBeDefined()
 })
})
