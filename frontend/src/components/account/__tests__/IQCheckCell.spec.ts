import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import IQCheckCell from '../IQCheckCell.vue'
import type { Account, IQTargetSummary } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const target = (model: string, patch: Partial<IQTargetSummary> = {}): IQTargetSummary => ({ model, enabled: true, verdict: 'smart', state: 'idle', gate_active: false, verdict_at: null, last_run_at: null, next_run_at: null, ...patch })
describe('IQ list cell', () => {
  it('tags each target and opens history separately from the switch', async () => {
    const account = { platform: 'openai', iq_targets: [target('gpt-6.1-sol', { verdict: 'degraded', gate_active: true }), target('gpt-6-astra'), target('custom', { enabled: false })] } as Account
    const wrapper = mount(IQCheckCell, { props: { account } })
    expect(wrapper.findAll('[data-testid="iq-target-tag"]').map(tag => tag.text())).toEqual(['Astra admin.accounts.iqCheckStatus.smart', 'Sol admin.accounts.iqCheckStatus.degraded·admin.accounts.iqCooling'])
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.emitted('records')).toHaveLength(1)
    expect(wrapper.emitted('toggle')).toBeUndefined()
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('toggle')).toEqual([[false]])
  })
  it('shows off when no built-in target is enabled and prevents duplicate toggles', async () => {
    const wrapper = mount(IQCheckCell, { props: { account: { platform: 'openai', iq_targets: [target('gpt-6-astra', { enabled: false })] } as Account, busy: true } })
    expect(wrapper.text()).toContain('iqOff')
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('toggle')).toBeUndefined()
  })
})
