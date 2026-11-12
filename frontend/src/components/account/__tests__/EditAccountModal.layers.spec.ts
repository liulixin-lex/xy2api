import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { getActivePinia } from 'pinia'

// The account edit form with the real dialog, proxy selector and model whitelist selector: their open lists are
// sub-layers of the dialog, so the first Escape closes only the list and the unsaved form stays (RT r3 R10).
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isSimpleMode: false, isAdmin: true }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { update: vi.fn(), checkMixedChannelRisk: vi.fn() },
    proxies: { testProxy: vi.fn() },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({})
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
  }
}))
vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: { syncUpstreamModels: vi.fn(), syncUpstreamModelsPreview: vi.fn() },
  getAntigravityDefaultModelMapping: vi.fn(),
  getIQCheck: vi.fn(),
  getIQCheckModels: vi.fn()
}))
vi.mock('@/api/admin/groupCategories', () => ({ default: { list: vi.fn(async () => []), save: vi.fn(), setGroupCategories: vi.fn() } }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key, te: () => false })
}))

import EditAccountModal from '../EditAccountModal.vue'

const GroupSelectorStub = defineComponent({ name: 'GroupSelector', props: { modelValue: { type: Array, default: () => [] } }, template: '<div />' })

const account = {
  id: 1, name: 'Relay Key', notes: '', platform: 'openai', type: 'apikey',
  credentials: { api_key: 'sk-test', base_url: 'https://api.openai.com', model_mapping: { 'gpt-5.2': 'gpt-5.2' } },
  extra: {}, proxy_id: null, concurrency: 1, priority: 1, rate_multiplier: 1, status: 'active', group_ids: [],
  expires_at: null, auto_pause_on_expired: false
} as any
const proxies = [{ id: 3, name: 'edge-1', protocol: 'http', host: '10.0.0.3', port: 8080, status: 'active' }] as any

enableAutoUnmount(afterEach)
beforeEach(() => {
  document.body.innerHTML = '<div id="app"></div>'
  document.body.className = ''
  document.documentElement.dataset.motion = 'reduced'
  getActivePinia()!.state.value.auth = { user: { id: 1, role: 'admin' }, runMode: 'standard' }
})
afterAll(() => { delete document.documentElement.dataset.motion })

async function openForm() {
  const wrapper = mount(EditAccountModal, {
    attachTo: '#app',
    props: { show: true, account, proxies, groups: [] },
    global: { stubs: { GroupSelector: GroupSelectorStub, Icon: true } }
  })
  await flushPromises()
  // An unsaved edit in the form.
  const name = document.querySelector<HTMLInputElement>('[data-tour="edit-account-form-name"]')!
  name.value = 'Relay Key (unsaved)'
  name.dispatchEvent(new Event('input'))
  await flushPromises()
  return { wrapper, name }
}

const escape = () => {
  const event = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  ;(document.activeElement ?? document.body).dispatchEvent(event)
}

describe('lists inside the account edit dialog', () => {
  it('proxy list: the first Escape closes the list and keeps the unsaved form, the second closes the dialog', async () => {
    const { wrapper, name } = await openForm()
    const block = document.querySelector('[data-testid="edit-account-proxy"]')!
    const trigger = block.querySelector<HTMLButtonElement>('button.select-trigger')!
    trigger.focus()
    trigger.click()
    await flushPromises()
    const search = block.querySelector<HTMLInputElement>('.select-search-input')!
    expect(document.activeElement).toBe(search)
    search.value = 'edge'
    search.dispatchEvent(new Event('input'))
    await flushPromises()

    escape()
    await flushPromises()
    // The list is leaving (jsdom never ends the leave transition).
    expect(trigger.classList.contains('select-trigger-open')).toBe(false)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(document.querySelector('[data-base-dialog]')).not.toBeNull()
    expect(name.isConnected && name.value).toBe('Relay Key (unsaved)')
    expect(document.activeElement).toBe(trigger)

    escape()
    await flushPromises()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('model whitelist: the first Escape closes the list and keeps the unsaved form, the second closes the dialog', async () => {
    const { wrapper, name } = await openForm()
    const box = document.querySelector<HTMLElement>('[data-base-dialog] .relative.mb-3 > div[tabindex="-1"]')!
    // A pointer click focuses the box (tabindex -1); jsdom's click() does not.
    box.focus()
    box.click()
    await flushPromises()
    const search = box.parentElement!.querySelector<HTMLInputElement>('input[type="text"]')!
    search.focus()
    search.value = 'gpt'
    search.dispatchEvent(new Event('input'))
    await flushPromises()
    expect(box.parentElement!.querySelectorAll('[data-testid="model-option"]').length).toBeGreaterThan(0)

    escape()
    await flushPromises()
    expect(box.parentElement!.querySelector('[data-testid="model-option"]')).toBeNull()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(name.value).toBe('Relay Key (unsaved)')
    expect(document.activeElement).toBe(box)

    escape()
    await flushPromises()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
