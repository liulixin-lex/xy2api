import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ manage: true })
})
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => perms.manage) }) }
})

enableAutoUnmount(afterEach)
beforeEach(() => { perms.manage = true })

const account = { id: 1, name: 'acc-1', platform: 'openai', type: 'oauth', status: 'error', schedulable: true } as Account

function open(props: { recoverable?: boolean; recovering?: boolean } = {}) {
  return mount(AccountActionMenu, {
    props: { show: true, account, anchorRect: new DOMRect(100, 100, 24, 24), ...props },
    global: { plugins: [getActivePinia()!], stubs: { Icon: true } },
    attachTo: document.body
  })
}
const items = () => Array.from(document.body.querySelectorAll<HTMLElement>('.action-menu-content button'))
const recoverItem = () => document.body.querySelector<HTMLElement>('[data-test="menu-recover-state"]')

describe('AccountActionMenu — recover state (contract delta F1, F2)', () => {
  it('leaves 恢复状态 out when nothing can be recovered', () => {
    open({ recoverable: false })
    expect(recoverItem()).toBeNull()
    expect(items()[0].textContent).toContain('admin.accounts.testConnection')
  })

  it('puts 恢复状态 first, followed by a divider, and emits it once with the account', () => {
    const wrapper = open({ recoverable: true })
    expect(items()[0]).toBe(recoverItem())
    expect(recoverItem()!.nextElementSibling!.tagName).toBe('DIV')
    recoverItem()!.click()
    expect(wrapper.emitted('recover-state')).toEqual([[account]])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('keeps 恢复状态 first but greyed while its recovery runs: a click only closes the menu', () => {
    const wrapper = open({ recoverable: true, recovering: true })
    expect(items()[0]).toBe(recoverItem())
    expect(recoverItem()!.getAttribute('aria-disabled')).toBe('true')
    recoverItem()!.click()
    expect(wrapper.emitted('recover-state')).toBeUndefined()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('offers neither 恢复状态 nor 删除 without accounts:manage', () => {
    perms.manage = false
    open({ recoverable: true })
    expect(recoverItem()).toBeNull()
    expect(document.body.querySelector('[data-test="menu-delete"]')).toBeNull()
  })

  it('ends with 删除 after a divider and emits it with the account', () => {
    const wrapper = open({ recoverable: true })
    const remove = document.body.querySelector<HTMLElement>('[data-test="menu-delete"]')!
    expect(items().at(-1)).toBe(remove)
    expect(remove.previousElementSibling!.tagName).toBe('DIV')
    remove.click()
    expect(wrapper.emitted('delete')).toEqual([[account]])
  })
})
