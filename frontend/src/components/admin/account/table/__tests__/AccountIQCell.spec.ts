import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountIQCell from '../AccountIQCell.vue'
import type { IQTargetSummary } from '@/types'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const target = (model: string, verdict: IQTargetSummary['verdict'], state: IQTargetSummary['state'] = 'idle'): IQTargetSummary =>
  ({ model, enabled: true, verdict, state, gate_active: false, verdict_at: null, last_run_at: null, next_run_at: null })

const mountCell = (targets: IQTargetSummary[]) =>
  mount(AccountIQCell, { props: { account: { id: 1, name: 'acc-1', platform: 'openai', iq_targets: targets, extra: {} } as never } })

describe('AccountIQCell', () => {
  it('lists every model with its recheck time in the card details, where there is no hover (spec 4.6)', () => {
    const later = new Date(Date.now() + 30 * 60_000).toISOString()
    const targets = [target('gpt-6-astra', 'smart'), { ...target('gpt-6.1-sol', 'smart'), next_run_at: later }, target('gpt-5', 'smart')]
    const wrapper = mount(AccountIQCell, { props: { detailed: true, account: { id: 1, name: 'acc-1', platform: 'openai', iq_targets: targets, extra: {} } as never } })
    expect(wrapper.findAll('[data-testid="iq-target-tag"]')).toHaveLength(3)
    expect(wrapper.find('[data-test="iq-more"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-test="iq-recheck"]').map((el) => el.text())[1]).toMatch(/^admin\.accounts\.table\.iqRecheckIn:/)
  })

  it('shows two models and counts the rest as +N, whose tip lists every model with its state; two or fewer have no +N', () => {
    const two = mountCell([target('gpt-6-astra', 'smart'), target('gpt-6.1-sol', 'smart')])
    expect(two.findAll('[data-testid="iq-target-tag"]')).toHaveLength(2)
    expect(two.find('[data-test="iq-more"]').exists()).toBe(false)

    const wrapper = mountCell([target('gpt-6-astra', 'smart'), target('gpt-6.1-sol', 'degraded'), target('gpt-5', 'smart', 'paused')])
    expect(wrapper.findAll('[data-testid="iq-target-tag"]').map((el) => el.text())).toEqual(['Astra', 'Sol'])
    const more = wrapper.get('[data-test="iq-more"]')
    expect(more.text()).toBe('admin.accounts.table.iqMore:{"n":1}')
    const tip = (more.element as HTMLElement & { __tipText?: string | (() => string) }).__tipText
    expect(typeof tip === 'function' ? tip() : tip).toBe([
      'admin.accounts.table.iqTip:{"model":"gpt-6-astra","state":"admin.accounts.table.iqStates.smart"}',
      'admin.accounts.table.iqTip:{"model":"gpt-6.1-sol","state":"admin.accounts.table.iqStates.degraded"}',
      'admin.accounts.table.iqTip:{"model":"gpt-5","state":"admin.accounts.table.iqStates.paused"}'
    ].join('\n'))
  })

  it('says only smart, degraded, checking (queued included) and paused; an unknown idle target shows no word (7d-12)', () => {
    const words = (targets: IQTargetSummary[]) =>
      mountCell(targets).findAll('.iq__v').map((el) => [el.text(), el.classes().find((cls) => cls.startsWith('iq__v--'))])
    expect(words([target('gpt-6-astra', 'smart'), target('gpt-6.1-sol', 'degraded')])).toEqual([
      ['admin.accounts.table.iqStates.smart', 'iq__v--smart'],
      ['admin.accounts.table.iqStates.degraded', 'iq__v--degraded']
    ])
    expect(words([target('gpt-6-astra', 'smart', 'queued'), target('gpt-6.1-sol', 'unknown', 'paused')])).toEqual([
      ['admin.accounts.table.iqStates.checking', 'iq__v--checking'],
      ['admin.accounts.table.iqStates.paused', 'iq__v--paused']
    ])
    const none = mountCell([target('gpt-6-astra', 'unknown')])
    expect(none.find('.iq__v').exists()).toBe(false)
    expect(none.get('.iq__none').text()).toBe('—')
  })
})
