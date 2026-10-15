import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GatewayResilienceSettings from '../GatewayResilienceSettings.vue'
import type { GatewayResilienceSettings as Settings } from '@/api/admin/resilience'

const mocks = vi.hoisted(() => ({ get: vi.fn(), save: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ RouterLink: { props: ['to'], template: '<a><slot /></a>' } }))
vi.mock('@/api/admin', () => ({ adminAPI: { resilience: { getSettings: mocks.get, updateSettings: mocks.save } } }))

function settings(): Settings {
  return {
    enabled: true,
    failure: { transient_cooldown_seconds: 30, retry_after_cap_seconds: 30, balance_keywords_extra: [] },
    session: { enabled: true, streak_threshold: 2, streak_window_seconds: 300, avoid_seconds: 600 },
    last_resort: { enabled: true },
    balance: {
      enabled: true, recheck_schedule_seconds: [60, 120, 300, 600, 900], jitter_ratio: 0.2, suspect_ttl_seconds: 300,
      recovering_concurrency: 1, recovering_window_seconds: 300, recovering_success_target: 3,
      generation_probe_daily_budget: 24, notify_after_hours: 2
    },
    budget: {
      enabled: true, max_attempts: 4, pre_output_budget_seconds: 240,
      first_output_timeout_seconds: { default: 180, high: 180, xhigh: 200, max: 220 },
      anthropic_first_event_timeout_seconds: 90, gemini_first_event_timeout_seconds: 90, model_overrides: {},
      same_account_retry_max: 1, gemini_same_account_retries: 1, upstream_retry_after_cap_seconds: 30
    },
    keepalive: { enabled: true, delay_seconds: 15, interval_seconds: 10, edges: ['cf', 'esa'], skip_official_oauth: true },
    stall: { enabled: true, text_stall_seconds: 120 },
    disconnect: {
      enabled: true, cancel_undelivered: true, partial_drain_max_seconds: 180,
      abuse_guard: { enabled: true, threshold_per_hour: 30, mode: 'bill_upstream_cost' }
    },
    errors: {
      enabled: true, no_account_retry_after: { min: 2, max: 30 }, config_retry_after_seconds: 60,
      reservation_retry_after_seconds: 5, negative_cache_seconds: 2,
      fast_fail: { enabled: true, window_seconds: 10, threshold: 3 }
    },
    fallback_group: { enabled: true, return_stable_seconds: 120, binding_ttl_seconds: 600 },
    reservation: { cache_aware_estimate: true, output_p99_cap: true }
  }
}

function mountSettings() {
  return mount(GatewayResilienceSettings, { global: { stubs: {
    Toggle: { props: ['modelValue', 'disabled'], emits: ['update:modelValue'], template: '<button type="button" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)">Toggle</button>' }
  } } })
}

describe('gateway resilience settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.get.mockResolvedValue({ settings: settings(), defaults: settings() })
    mocks.save.mockImplementation(async (payload: Settings) => ({ settings: payload, defaults: settings() }))
  })

  it('saves a section switch and a numeric field as one document', async () => {
    const w = mountSettings(); await flushPromises()
    const save = w.get('[data-testid="resilience-save"]')
    expect(save.attributes('disabled')).toBeDefined()
    await w.get('[data-testid="resilience-section-keepalive"]').trigger('click')
    await w.get('[data-testid="resilience-field-maxAttempts"]').setValue('3')
    await w.get('[data-testid="resilience-field-edges"]').setValue('CF, esa ,')
    await save.trigger('click'); await flushPromises()
    const expected = settings()
    expected.keepalive.enabled = false
    expected.keepalive.edges = ['cf', 'esa']
    expected.budget.max_attempts = 3
    expect(mocks.save).toHaveBeenCalledWith(expected)
    expect(w.text()).toContain('admin.resilience.settings.saved')
  })

  it('disables the sections while the master switch is off', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="resilience-enabled"]').trigger('click')
    expect(w.get('[data-testid="resilience-section-budget"]').attributes('disabled')).toBeDefined()
  })

  it('rejects an out-of-range number before saving', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="resilience-field-preOutputBudget"]').setValue('600')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(mocks.save).not.toHaveBeenCalled()
    expect(w.text()).toContain('admin.resilience.settings.invalidNumber')
  })

  it('applies the advanced JSON and refuses a malformed document', async () => {
    const w = mountSettings(); await flushPromises()
    const next = settings()
    next.stall.text_stall_seconds = 90
    await w.get('[data-testid="resilience-json"]').setValue(JSON.stringify(next))
    await w.get('[data-testid="resilience-apply-json"]').trigger('click')
    expect((w.get('[data-testid="resilience-field-textStall"]').element as HTMLInputElement).value).toBe('90')
    await w.get('[data-testid="resilience-json"]').setValue('{"enabled":true}')
    await w.get('[data-testid="resilience-apply-json"]').trigger('click')
    expect(w.text()).toContain('admin.resilience.settings.invalidJson')
  })

  it('shows the server error and keeps the draft when saving fails', async () => {
    mocks.save.mockRejectedValueOnce({ status: 400, message: 'invalid budget' })
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="resilience-section-stall"]').trigger('click')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('invalid budget')
    expect(w.get('[data-testid="resilience-save"]').attributes('disabled')).toBeUndefined()
  })

  it('offers a reload when the settings cannot be read', async () => {
    mocks.get.mockRejectedValueOnce(new Error('down'))
    const w = mountSettings(); await flushPromises()
    expect(w.find('[data-testid="resilience-save"]').exists()).toBe(false)
    await w.findAll('button').find(b => b.text() === 'admin.resilience.settings.reload')!.trigger('click'); await flushPromises()
    expect(w.find('[data-testid="resilience-save"]').exists()).toBe(true)
  })
})
