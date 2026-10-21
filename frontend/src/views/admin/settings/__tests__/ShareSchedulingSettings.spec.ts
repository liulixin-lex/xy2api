import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ShareSchedulingSettings from '../ShareSchedulingSettings.vue'

const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } }))
vi.mock('@/api/admin', () => ({ adminAPI: { shareScheduling: { getSettings: mocks.get } } }))
vi.mock('@/api/admin/shareScheduling', () => ({ default: { updateSettings: mocks.save } }))

function mountSettings() {
  return mount(ShareSchedulingSettings, {
    global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-dialog>{{ title }}<slot /><slot name="footer" /></div>' } } }
  })
}

describe('share scheduling settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.get.mockResolvedValue({ enabled: false })
    mocks.save.mockImplementation(async (payload: { enabled: boolean }) => payload)
  })

  it('shows only the main switch and the page link', async () => {
    const w = mountSettings(); await flushPromises()
    expect(w.find('[data-testid="share-global-toggle"]').exists()).toBe(true)
    expect(w.find('a').attributes('href')).toBe('/admin/share-scheduling')
    expect(w.text()).not.toContain('sampleRate')
    expect(w.text()).not.toContain('decisionSamples')
  })

  it('turns the main switch on after confirming', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="share-global-toggle"]').trigger('click')
    expect(w.text()).toContain('admin.shareScheduling.global.onTitle')
    expect(mocks.save).not.toHaveBeenCalled()
    await w.get('[data-testid="share-global-confirm"]').trigger('click'); await flushPromises()
    expect(mocks.save).toHaveBeenCalledWith({ enabled: true })
    expect(w.get('[data-testid="share-global-toggle"]').attributes('aria-checked')).toBe('true')
    expect(w.find('[data-dialog]').exists()).toBe(false)
  })

  it('keeps the dialog open with the error when saving fails', async () => {
    mocks.save.mockRejectedValueOnce({ message: 'nope' })
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="share-global-toggle"]').trigger('click')
    await w.get('[data-testid="share-global-confirm"]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('nope')
    expect(w.get('[data-testid="share-global-toggle"]').attributes('aria-checked')).toBe('false')
  })

  it('offers a reload when the switch cannot be read', async () => {
    mocks.get.mockRejectedValueOnce(new Error('down'))
    const w = mountSettings(); await flushPromises()
    expect(w.text()).toContain('admin.shareScheduling.global.loadFailed')
    mocks.get.mockResolvedValueOnce({ enabled: true })
    await w.get('button').trigger('click'); await flushPromises()
    expect(w.get('[data-testid="share-global-toggle"]').attributes('aria-checked')).toBe('true')
  })
})
