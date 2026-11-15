import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountIQCell from '../AccountIQCell.vue'
import { formatShortTime } from '../accountTime'
import type { IQTargetSummary } from '@/types'
import type { TipCard } from '@/utils/tip'

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

  it('shows two models and counts the rest as +N; two or fewer have no +N', () => {
    const two = mountCell([target('gpt-6-astra', 'smart'), target('gpt-6.1-sol', 'smart')])
    expect(two.findAll('[data-testid="iq-target-tag"]')).toHaveLength(2)
    expect(two.find('[data-test="iq-more"]').exists()).toBe(false)

    const wrapper = mountCell([target('gpt-6-astra', 'smart'), target('gpt-6.1-sol', 'degraded'), target('gpt-5', 'smart', 'paused')])
    expect(wrapper.findAll('[data-testid="iq-target-tag"]').map((el) => el.text())).toEqual(['Astra', 'Sol'])
    expect(wrapper.get('[data-test="iq-more"]').text()).toBe('admin.accounts.table.iqMore:{"n":1}')
  })

  // One tip card on the records button (12.7), "+N" and the words have none of their own.
  it('lists every model in its card, the folded ones too, with state, last check and recheck', () => {
    const last = new Date(Date.now() - 2 * 86_400_000).toISOString()
    const wrapper = mountCell([
      { ...target('gpt-6-astra', 'smart'), last_run_at: last, next_run_at: new Date(Date.now() + 3 * 60_000).toISOString() },
      { ...target('gpt-6.1-sol', 'degraded'), next_run_at: new Date(Date.now() - 60_000).toISOString() },
      target('gpt-5', 'smart', 'running'),
      { ...target('gpt-5.1', 'smart', 'paused'), next_run_at: new Date(Date.now() + 60_000).toISOString() }
    ])
    const tipOf = (el: Element) => (el as HTMLElement & { __tipText?: () => TipCard }).__tipText
    expect(wrapper.findAll('.iq__v, [data-test="iq-more"]').map((el) => tipOf(el.element))).toEqual([undefined, undefined, undefined])
    expect(tipOf(wrapper.get('[data-test="iq-lines"]').element)!()).toEqual({
      head: ['admin.accounts.table.tip.iqModel', 'admin.accounts.table.tip.iqState', 'admin.accounts.table.tip.iqLastRun', 'admin.accounts.table.tip.iqRecheck'],
      rows: [
        ['gpt-6-astra', { text: 'admin.accounts.table.iqStates.smart', tone: 'ok' }, formatShortTime(last), 'admin.accounts.table.tip.iqIn:{"time":"common.time.countdown.minutes:{\\"m\\":3}"}'],
        ['gpt-6.1-sol', { text: 'admin.accounts.table.iqStates.degraded', tone: 'error' }, '—', 'admin.accounts.table.tip.iqDue'],
        ['gpt-5', { text: 'admin.accounts.table.iqStates.checking', tone: 'info' }, '—', '—'],
        ['gpt-5.1', { text: 'admin.accounts.table.iqStates.paused', tone: undefined }, '—', '—']
      ]
    })
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
