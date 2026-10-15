import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsResilienceExplain from '../OpsResilienceExplain.vue'

const mocks = vi.hoisted(() => ({ explain: vi.fn(), groups: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (d: string) => d }))
vi.mock('@/api/admin', () => ({ adminAPI: { resilience: { explain: mocks.explain }, groups: { getAll: mocks.groups } } }))

const SelectStub = {
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<select @change="$emit(\'update:modelValue\', Number($event.target.value))"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>'
}

function mountExplain() {
  return mount(OpsResilienceExplain, { global: { stubs: { Select: SelectStub, ResilienceExplainResult: { props: ['explanation'], template: '<div data-testid="result">{{ explanation.group_id }}</div>' } } } })
}

describe('ops resilience explain', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.groups.mockResolvedValue([{ id: 3, name: 'codex', platform: 'openai' }, { id: 7, name: 'claude', platform: 'anthropic' }])
    mocks.explain.mockResolvedValue({ group_id: 7, enabled: true, healthy_count: 1, accounts: [] })
  })

  it('explains the chosen group with the optional model and session', async () => {
    const w = mountExplain(); await flushPromises()
    expect(w.get('[data-testid="resilience-explain-group"]').text()).toContain('claude #7')
    expect(w.get('[data-testid="resilience-explain-run"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="resilience-explain-group"]').setValue('7')
    await w.get('[data-testid="resilience-explain-model"]').setValue('gpt-6.1-sol')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(mocks.explain).toHaveBeenCalledWith(7, { model: 'gpt-6.1-sol' })
    expect(w.get('[data-testid="result"]').text()).toBe('7')

    await w.get('[data-testid="resilience-explain-session"]').setValue('sess')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(mocks.explain).toHaveBeenLastCalledWith(7, { model: 'gpt-6.1-sol', session_hash: 'sess' })

    await w.get('[data-testid="resilience-explain-group"]').setValue('3')
    expect(w.find('[data-testid="result"]').exists()).toBe(false)
  })

  it('reports failures', async () => {
    mocks.explain.mockRejectedValueOnce({ status: 503, message: 'explain down' })
    const w = mountExplain(); await flushPromises()
    await w.get('[data-testid="resilience-explain-group"]').setValue('3')
    await w.get('form').trigger('submit'); await flushPromises()
    expect(w.get('[role="alert"]').text()).toContain('explain down')
  })
})
