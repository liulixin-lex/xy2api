import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ShareSchedulingSettings from '../ShareSchedulingSettings.vue'

const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ RouterLink: { props: ['to'], template: '<a><slot /></a>' } }))
vi.mock('@/api/admin', () => ({ adminAPI: { shareScheduling: { getSettings: mocks.get, updateSettings: mocks.save } } }))

const defaults = { enabled: false, cache_attribution: true, attribution_sample_rate: 4, decision_samples: true }

function mountSettings() {
  return mount(ShareSchedulingSettings, { global: { stubs: {
    Toggle: { props: ['modelValue', 'disabled'], emits: ['update:modelValue'], template: '<button type="button" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)">Toggle</button>' }
  } } })
}

describe('share scheduling settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.get.mockResolvedValue({ ...defaults })
    mocks.save.mockImplementation(async payload => ({ ...defaults, ...payload }))
  })

  it('turns the master switch on and saves every field', async () => {
    const w = mountSettings(); await flushPromises()
    const save = w.get('[data-testid="share-settings-save"]')
    expect(save.attributes('disabled')).toBeDefined()
    await w.get('[data-testid="share-enabled"]').trigger('click')
    await w.get('[data-testid="share-sample-rate"]').setValue('8')
    await save.trigger('click'); await flushPromises()
    expect(mocks.save).toHaveBeenCalledWith({ enabled: true, cache_attribution: true, attribution_sample_rate: 8, decision_samples: true })
    expect(w.text()).toContain('settings.saved')
    expect(save.attributes('disabled')).toBeDefined()
  })

  it('rejects an out-of-range sampling rate before saving', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="share-sample-rate"]').setValue('0')
    await w.get('[data-testid="share-settings-save"]').trigger('click'); await flushPromises()
    expect(mocks.save).not.toHaveBeenCalled()
    expect(w.text()).toContain('settings.sampleRateInvalid')
  })

  it('keeps the draft when saving fails', async () => {
    mocks.save.mockRejectedValueOnce({ status: 500, message: 'down' })
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="share-enabled"]').trigger('click')
    await w.get('[data-testid="share-settings-save"]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('down')
    expect(w.get('[data-testid="share-settings-save"]').attributes('disabled')).toBeUndefined()
  })

  it('offers a reload when the settings cannot be read', async () => {
    mocks.get.mockRejectedValueOnce(new Error('down'))
    const w = mountSettings(); await flushPromises()
    expect(w.find('[data-testid="share-settings-save"]').exists()).toBe(false)
    await w.findAll('button').find(b => b.text() === 'admin.shareScheduling.settings.reload')!.trigger('click'); await flushPromises()
    expect(w.find('[data-testid="share-settings-save"]').exists()).toBe(true)
  })
})
