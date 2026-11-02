import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key })
}))

describe('AccountActionMenu — group quality gate recovery', () => {
  it('offers recover state for a gate-only state and emits recover-state', async () => {
    const account = { id: 1, platform: 'openai', type: 'oauth', status: 'active', schedulable: true } as Account
    const wrapper = mount(AccountActionMenu, {
      props: { show: true, account, anchorRect: new DOMRect(100, 100, 24, 24), qualityRecoverable: true },
      // 全局 setup 每条用例创建并激活 pinia；挂到应用上，组件里的 auth、权限 store 不再告警。
      global: { plugins: [getActivePinia()!], stubs: { Icon: true } },
      attachTo: document.body
    })
    const button = Array.from(document.body.querySelectorAll('button')).find(b => b.textContent?.includes('admin.accounts.recoverState'))
    expect(button).toBeDefined()
    button!.click()
    expect(wrapper.emitted('recover-state')?.[0][0]).toMatchObject({ id: 1 })
    wrapper.unmount()
  })
})
