import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import IQCheckCell from '../IQCheckCell.vue'
import type { Account, IQTargetSummary } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const target = (model: string, patch: Partial<IQTargetSummary> = {}): IQTargetSummary => ({ model, enabled: true, verdict: 'smart', state: 'idle', gate_active: false, verdict_at: null, last_run_at: null, next_run_at: null, ...patch })
const rows = (wrapper: ReturnType<typeof mount>) => wrapper.findAll('[data-testid="iq-target-tag"]').map(tag => [tag.get('[data-part="name"]').text(), tag.get('[data-part="state"]').text()])
describe('IQ list cell', () => {
  it('tags each target and opens history separately from the switch', async () => {
    const account = { platform: 'openai', iq_targets: [target('gpt-6.1-sol', { verdict: 'degraded', gate_active: true }), target('gpt-6-astra'), target('custom', { enabled: false })] } as Account
    const wrapper = mount(IQCheckCell, { props: { account } })
    // The cooldown is shown in the status column; the IQ cell only names the verdict.
    expect(rows(wrapper)).toEqual([['Astra', 'admin.accounts.iqCheckStatus.smart'], ['Sol', 'admin.accounts.iqCheckStatus.degraded']])
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.emitted('records')).toHaveLength(1)
    expect(wrapper.emitted('toggle')).toBeUndefined()
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('toggle')).toEqual([[false]])
  })
  it('stacks one row per target and folds a fourth into +N coloured by the worst hidden target', () => {
    const three = { platform: 'openai', iq_targets: [target('gpt-6-astra', { state: 'running' }), target('gpt-6.1-sol', { state: 'queued' }), target('custom', { verdict: 'unknown' })] } as Account
    const wrapper = mount(IQCheckCell, { props: { account: three } })
    expect(rows(wrapper)).toEqual([['Astra', 'admin.accounts.iqStates.running'], ['Sol', 'admin.accounts.iqStates.queued'], ['custom', 'admin.accounts.iqCheckStatus.unknown']])
    expect(wrapper.findAll('[data-testid="iq-target-tag"] .animate-spin')).toHaveLength(1)
    expect(wrapper.find('[data-testid="iq-target-more"]').exists()).toBe(false)

    const four = { platform: 'openai', iq_targets: [...three.iq_targets!, target('custom-2', { verdict: 'degraded' })] } as Account
    const folded = mount(IQCheckCell, { props: { account: four } })
    expect(rows(folded).map(([name]) => name)).toEqual(['Astra', 'Sol'])
    const more = folded.get('[data-testid="iq-target-more"]')
    expect(more.text()).toBe('+2')
    expect(more.classes()).toContain('text-red-700')
  })
  it('shows off when no built-in target is enabled and prevents duplicate toggles', async () => {
    const wrapper = mount(IQCheckCell, { props: { account: { platform: 'openai', iq_targets: [target('gpt-6-astra', { enabled: false })] } as Account, busy: true } })
    expect(wrapper.text()).toContain('iqOff')
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('toggle')).toBeUndefined()
  })
})
