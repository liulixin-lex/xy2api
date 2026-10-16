import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChannelMonitor } from '@/api/admin/channelMonitor'
import type { ChannelMonitorTemplate } from '@/api/admin/channelMonitorTemplate'
import MonitorFormDialog from '../MonitorFormDialog.vue'
import MonitorTemplateManagerDialog from '../MonitorTemplateManagerDialog.vue'
import MonitorAdvancedRequestConfig from '../MonitorAdvancedRequestConfig.vue'

const api = vi.hoisted(() => ({
  monitorCreate: vi.fn(), monitorUpdate: vi.fn(), templateList: vi.fn(),
  templateCreate: vi.fn(), templateUpdate: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: {
  channelMonitor: { create: api.monitorCreate, update: api.monitorUpdate },
  channelMonitorTemplate: { list: api.templateList, create: api.templateCreate, update: api.templateUpdate },
  accounts: { list: vi.fn().mockResolvedValue({ items: [] }), getById: vi.fn() },
} }))
vi.mock('@/api/keys', () => ({ keysAPI: { list: vi.fn() } }))
vi.mock('@/api/groups', () => ({ userGroupsAPI: { getUserGroupRates: vi.fn() } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: null, showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const BaseDialog = defineComponent({
  props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})
const Select = defineComponent({
  props: ['options', 'modelValue'], emits: ['update:modelValue'],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
})
const stubs = { BaseDialog, Select, Toggle: true, ModelTagInput: true, MonitorKeyPickerDialog: true, MonitorTemplateApplyPickerDialog: true, ConfirmDialog: true }
const wrappers: VueWrapper[] = []
afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()) })
beforeEach(() => {
  vi.clearAllMocks()
  api.templateList.mockResolvedValue({ items: [] })
})

function monitor(): ChannelMonitor {
  return {
    id: 70, name: 'Grok', provider: 'grok', api_mode: 'responses', check_mode: 'probe', account_id: null,
    endpoint: 'https://gateway.example', api_key_masked: 'fixture***', primary_model: 'grok-4.6', extra_models: [],
    group_name: '', enabled: true, interval_seconds: 60, jitter_seconds: 0, last_checked_at: null,
    created_by: 1, created_at: '', updated_at: '', primary_status: '', primary_latency_ms: null,
    availability_7d: 0, extra_models_status: [], template_id: null, extra_headers: {},
    body_override_mode: 'off', body_override: null,
  }
}
function template(id: number, api_mode: 'responses' | 'chat_completions', provider: 'grok' | 'openai' = 'grok'): ChannelMonitorTemplate {
  return { id, provider, api_mode, name: `${provider}-${api_mode}`, description: '', extra_headers: {},
    body_override_mode: 'merge', body_override: { max_output_tokens: 80 }, created_at: '', updated_at: '', associated_monitors: 0 }
}
function form(saved: ChannelMonitor | null = null) {
  const w = mount(MonitorFormDialog, { props: { show: true, monitor: saved }, global: { stubs } })
  wrappers.push(w)
  return w
}
async function click(w: VueWrapper, text: string) {
  const button = w.findAll('button').find(b => b.text().includes(text))
  expect(button, text).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

describe('Grok Responses monitoring', () => {
  it('creates a Responses probe while retaining the Grok provider and key', async () => {
    const w = form()
    await flushPromises()
    await w.get('[data-testid="monitor-provider-grok"]').trigger('click')
    await click(w, 'admin.channelMonitor.form.apiModeResponses')
    await w.get('input[placeholder="admin.channelMonitor.form.namePlaceholder"]').setValue('Grok Responses')
    await w.get('input[type="password"]').setValue('grok-fixture-key')
    await w.get('[data-testid="monitor-primary-model"]').setValue('grok-4.6')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.monitorCreate).toHaveBeenCalledWith(expect.objectContaining({
      provider: 'grok', api_mode: 'responses', check_mode: 'probe', api_key: 'grok-fixture-key', primary_model: 'grok-4.6',
    }))
  })

  it('round-trips an existing Responses monitor without replacing its saved key', async () => {
    const w = form(monitor())
    await flushPromises()
    expect(w.findAll('button').find(b => b.text().includes('admin.channelMonitor.form.apiModeResponses'))?.attributes('aria-pressed')).toBe('true')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.monitorUpdate).toHaveBeenLastCalledWith(70, expect.objectContaining({ provider: 'grok', api_mode: 'responses' }))
    expect(api.monitorUpdate.mock.calls[0][1]).not.toHaveProperty('api_key')
    await click(w, 'admin.channelMonitor.form.apiModeChatCompletions')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.monitorUpdate).toHaveBeenLastCalledWith(70, expect.objectContaining({ provider: 'grok', api_mode: 'chat_completions' }))
  })

  it('filters Grok templates by protocol and clears a Responses snapshot when switching to Chat', async () => {
    api.templateList.mockResolvedValue({ items: [template(1, 'chat_completions'), template(2, 'responses'), template(3, 'responses', 'openai')] })
    const w = form(monitor())
    await flushPromises()
    expect(w.get('select').findAll('option').map(o => o.attributes('value'))).toEqual(['', '2'])
    await w.get('select').setValue('2')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.monitorUpdate).toHaveBeenLastCalledWith(70, expect.objectContaining({ api_mode: 'responses', template_id: 2, body_override: { max_output_tokens: 80 } }))
    await click(w, 'admin.channelMonitor.form.apiModeChatCompletions')
    expect(w.get('select').findAll('option').map(o => o.attributes('value'))).toEqual(['', '1'])
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.monitorUpdate).toHaveBeenLastCalledWith(70, expect.objectContaining({ api_mode: 'chat_completions', clear_template: true, body_override_mode: 'off', body_override: null }))
  })

  it('resets the protocol when switching to a provider without Responses support', async () => {
    const w = form(monitor())
    await flushPromises()
    await w.get('[data-testid="monitor-provider-anthropic"]').trigger('click')
    expect(w.text()).not.toContain('admin.channelMonitor.form.apiModeResponses')
    await w.get('form').trigger('submit')
    await flushPromises()
    expect(api.monitorUpdate).toHaveBeenCalledWith(70, expect.objectContaining({ provider: 'anthropic', api_mode: 'chat_completions' }))
  })

  it('creates and edits native Grok Responses templates', async () => {
    const w = mount(MonitorTemplateManagerDialog, { props: { show: true }, global: { stubs } })
    wrappers.push(w)
    await flushPromises()
    await click(w, 'monitorCommon.providers.grok')
    await click(w, 'admin.channelMonitor.template.createButton')
    await click(w, 'admin.channelMonitor.form.apiModeResponses')
    await w.get('input[placeholder="admin.channelMonitor.template.form.namePlaceholder"]').setValue('Grok Responses')
    api.templateList.mockResolvedValue({ items: [template(2, 'responses')] })
    await click(w, 'common.create')
    expect(api.templateCreate).toHaveBeenCalledWith(expect.objectContaining({ provider: 'grok', api_mode: 'responses' }))
    await click(w, 'common.edit')
    await click(w, 'common.update')
    expect(api.templateUpdate).toHaveBeenCalledWith(2, expect.objectContaining({ api_mode: 'responses' }))
  })

  it('shows a Grok Responses request example with input instead of messages', () => {
    const w = mount(MonitorAdvancedRequestConfig, { props: { provider: 'grok', apiMode: 'responses', extraHeaders: {}, bodyOverrideMode: 'replace', bodyOverride: null } })
    wrappers.push(w)
    const example = JSON.parse(w.get('textarea').attributes('placeholder'))
    expect(example.model).toMatch(/^grok-/)
    expect(example).toHaveProperty('input')
    expect(example).toHaveProperty('instructions')
    expect(example.stream).toBe(false)
    expect(example).not.toHaveProperty('messages')
  })
})
