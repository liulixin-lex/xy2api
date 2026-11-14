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
  it('shows two models and counts the rest as +N, whose tip lists every model with its state', () => {
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

  it('has no +N with two models or fewer', () => {
    const wrapper = mountCell([target('gpt-6-astra', 'smart'), target('gpt-6.1-sol', 'smart')])
    expect(wrapper.findAll('[data-testid="iq-target-tag"]')).toHaveLength(2)
    expect(wrapper.find('[data-test="iq-more"]').exists()).toBe(false)
  })
})
