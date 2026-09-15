import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import IQCheckCell from '../IQCheckCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
describe('IQ list cell', () => {
  it('keeps execution details in history and opens it separately from the toggle', async () => {
    const account = { platform: 'openai', iq_check: { enabled: true, status: 'smart', execution_state: 'deferred', execution_reason: 'account_busy', freshness: 'stale', next_eligible_at: '2026-09-15T13:04:17Z' } } as Account
    const wrapper = mount(IQCheckCell, { props: { account } })
    expect(wrapper.text()).toContain('iqCheckStatus.smart')
    expect(wrapper.text()).not.toContain('account_busy')
    expect(wrapper.text()).not.toContain('2026-09-15')
    await wrapper.findAll('button')[1].trigger('click')
    expect(wrapper.emitted('records')).toHaveLength(1)
    expect(wrapper.emitted('toggle')).toBeUndefined()
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('toggle')).toHaveLength(1)
  })
  it('shows off without retaining a stale smart label and prevents duplicate toggles', async () => {
    const wrapper = mount(IQCheckCell, { props: { account: { platform: 'openai', iq_check: { enabled: false, status: 'smart' } } as Account, busy: true } })
    expect(wrapper.text()).toContain('iqOff')
    expect(wrapper.text()).not.toContain('iqCheckStatus.smart')
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('toggle')).toBeUndefined()
  })
})
