import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'

import AccountBulkActionsBar from '../AccountBulkActionsBar.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

enableAutoUnmount(afterEach)

const base = { selectedIds: [1], totalResults: 45, selectingAll: false, allResultsSelected: false }
const mountBar = (props: Record<string, unknown> = {}) =>
  mount(AccountBulkActionsBar, { props: { ...base, ...props }, attachTo: document.body, global: { stubs: { Icon: true } } })
const menuItems = () => Array.from(document.body.querySelectorAll<HTMLElement>('[data-test="bulk-more-menu"] [role="menuitem"]'))

describe('AccountBulkActionsBar', () => {
  it('disables both scheduling actions during a pending selected-account update', async () => {
    const wrapper = mountBar({ totalResults: 1, allResultsSelected: true, schedulingBusy: true })
    const buttons = wrapper.findAll('button').filter(button => /enableScheduling|disableScheduling/.test(button.text()))
    expect(buttons).toHaveLength(2)
    for (const button of buttons) {
      expect(button.attributes('disabled')).toBeDefined()
      expect(button.attributes('aria-busy')).toBe('true')
      await button.trigger('click')
    }
    expect(wrapper.emitted('toggle-schedulable')).toBeUndefined()
    await wrapper.setProps({ schedulingBusy: false })
    await buttons[0].trigger('click')
    expect(wrapper.emitted('toggle-schedulable')).toEqual([[true]])
  })

  it('selects all results from a partial selection', async () => {
    const wrapper = mountBar()
    const button = wrapper.findAll('button').find(item => item.text().includes('admin.accounts.bulkActions.selectAllResults'))
    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('select-all-results')).toHaveLength(1)
  })

  it('groups the actions as 恢复 豁免 | 启用调度 停止调度 | 编辑 更多, without 批量更新', () => {
    const wrapper = mountBar()
    const actions = wrapper.findAll('[data-test="bulk-actions"] > *').map(el => (el.element.tagName === 'SPAN' ? '|' : el.text()))
    expect(actions).toEqual([
      'admin.schedQuality.action.reset', 'admin.schedQuality.action.exempt', '|',
      'admin.accounts.bulkActions.enableScheduling', 'admin.accounts.bulkActions.disableScheduling', '|',
      'admin.accounts.bulkActions.edit', 'common.more'
    ])
    expect(wrapper.text()).not.toContain('admin.accounts.bulkEdit.submit')
  })

  it('keeps 重置状态, 刷新令牌, 探测上游倍率, 智商检测 and 删除 under 更多', async () => {
    const wrapper = mountBar()
    expect(menuItems()).toEqual([])
    await wrapper.get('[data-test="bulk-more"]').trigger('click')
    expect(wrapper.get('[data-test="bulk-more"]').attributes('aria-expanded')).toBe('true')
    expect(menuItems().map(item => item.textContent)).toEqual([
      'admin.accounts.bulkActions.resetStatus', 'admin.accounts.bulkActions.refreshToken', 'admin.accounts.bulkActions.probeUpstreamBilling',
      'admin.accounts.bulkActions.iqCheck', 'admin.accounts.bulkActions.delete'
    ])
    const events = ['reset-status', 'refresh-token', 'probe-upstream-billing', 'run-iq-check', 'delete']
    for (const [index, event] of events.entries()) {
      menuItems()[index].click()
      await wrapper.vm.$nextTick()
      expect(wrapper.emitted(event)).toHaveLength(1)
      // A pick closes the menu.
      expect(menuItems()).toEqual([])
      await wrapper.get('[data-test="bulk-more"]').trigger('click')
    }
  })

  it('greys only 恢复 while a recovery runs, and every quality action while a quality batch runs', async () => {
    const wrapper = mountBar({ recoverBusy: true })
    const disabled = () => ['reset', 'exempt'].map(action => wrapper.get(`[data-test="bulk-sched-${action}"]`).attributes('disabled') !== undefined)
    expect(disabled()).toEqual([true, false])
    expect(wrapper.get('[data-test="bulk-sched-reset"]').attributes('aria-busy')).toBe('true')
    await wrapper.setProps({ recoverBusy: false, schedBusy: true })
    expect(disabled()).toEqual([true, true])
  })
})
