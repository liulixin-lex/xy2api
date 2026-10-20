import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import CommandPalette from '../CommandPalette.vue'
import { registerCommandEntries, useCommandPalette } from '@/composables/useCommandPalette'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const Page = defineComponent({ render: () => h('div') })
const mounted: Array<{ unmount: () => void }> = []

async function setup() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/admin/dashboard', component: Page },
      { path: '/admin/accounts', component: Page },
      { path: '/admin/groups', component: Page }
    ]
  })
  await router.push('/admin/dashboard')
  const Host = defineComponent({
    setup() {
      registerCommandEntries(() => [
        { path: '/admin/dashboard', label: 'Dashboard' },
        { path: '/admin/accounts', label: 'Accounts' },
        { path: '/admin/groups', label: 'Groups' }
      ])
      return () => h(CommandPalette)
    }
  })
  const wrapper = mount(Host, { attachTo: document.body, global: { plugins: [router], stubs: { Icon: true } } })
  mounted.push(wrapper)
  return { wrapper, router }
}

const press = (key: string, init: KeyboardEventInit = {}) =>
  window.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }))

describe('CommandPalette', () => {
  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    useCommandPalette().closePalette()
    document.body.innerHTML = ''
  })

  it('opens with Ctrl+K, filters, moves with arrows and navigates on Enter', async () => {
    const { router } = await setup()
    press('k', { ctrlKey: true })
    await flushPromises()

    const input = document.querySelector<HTMLInputElement>('[data-test="command-palette-input"]')!
    expect(input).not.toBeNull()
    expect(document.activeElement).toBe(input)
    expect(document.querySelectorAll('[data-test="command-palette-option"]')).toHaveLength(3)

    input.value = 'a'
    input.dispatchEvent(new Event('input'))
    await flushPromises()
    const options = () => Array.from(document.querySelectorAll('[data-test="command-palette-option"]'))
    // 标题前缀命中排最前，其余按原顺序
    expect(options().map((el) => el.querySelector('span')?.textContent?.trim())).toEqual(['Accounts', 'Dashboard', 'Groups'])
    expect(options()[1].textContent).toContain('commandPalette.current')

    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await flushPromises()
    expect(options()[1].getAttribute('aria-selected')).toBe('true')
    expect(input.getAttribute('aria-activedescendant')).toBe(options()[1].id)

    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true }))
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/admin/accounts')
    expect(useCommandPalette().open.value).toBe(false)
  })

  it('does not open over another modal dialog', async () => {
    await setup()
    const other = document.createElement('div')
    other.setAttribute('aria-modal', 'true')
    document.body.appendChild(other)
    press('k', { metaKey: true })
    await flushPromises()
    expect(useCommandPalette().open.value).toBe(false)
  })

  it('closes on Escape', async () => {
    await setup()
    useCommandPalette().openPalette()
    await flushPromises()
    const input = document.querySelector<HTMLInputElement>('[data-test="command-palette-input"]')!
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await flushPromises()
    expect(useCommandPalette().open.value).toBe(false)
  })
})
