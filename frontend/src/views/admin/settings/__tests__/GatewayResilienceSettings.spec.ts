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
    failure: { transient_cooldown_seconds: 30, retry_after_cap_seconds: 30, balance_keywords_extra: [], decision_v2: true },
    session: { enabled: true, streak_threshold: 2, streak_window_seconds: 300, avoid_seconds: 600, post_output_rebind: true },
    last_resort: { enabled: true },
    anthropic_latency_hold: { enabled: true },
    same_site_hold: { enabled: true },
    balance: { enabled: true, pause_seconds: 300 },
    budget: {
      enabled: true, max_attempts: 4, pre_output_budget_seconds: 240, budget_start: 'arrival', budget_cut_attribution: 'observe',
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
      fast_fail: { enabled: true, window_seconds: 10, threshold: 3 },
      turn_cap: { enabled: true, failures: 5, window_seconds: 120 },
      failed_frame_retry_after: true, codex_retry_after_cap_seconds: 10
    },
    fallback_group: { enabled: true, return_stable_seconds: 120, binding_ttl_seconds: 600 },
    reservation: { cache_aware_estimate: true, output_p99_cap: true },
    stream: { prelude_staging: true, native_prelude_whitelist: true, strict_terminal: true },
    capability: { stream_only_learning: true, learn_ttl_seconds: 21600 },
    collaboration_plaintext: { enabled: false, models: [] },
    fco_compat: { mode: 'reactive', admin_names: [] }
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

  it('saves deadline and attribution controls independently', async () => {
    const w = mountSettings(); await flushPromises()
    expect((w.get('[data-testid="resilience-field-budgetStart"]').element as HTMLSelectElement).value).toBe('arrival')
    expect((w.get('[data-testid="resilience-field-budgetAttribution"]').element as HTMLSelectElement).value).toBe('observe')
    await w.get('[data-testid="resilience-field-budgetStart"]').setValue('body_eof')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(mocks.save.mock.calls[0][0].budget.budget_start).toBe('body_eof')
    expect(mocks.save.mock.calls[0][0].budget.budget_cut_attribution).toBe('observe')
  })

  it('edits the initial balance pause with a tiered pause hint in the balance card', async () => {
    const w = mountSettings(); await flushPromises()
    const card = w.get('[data-testid="resilience-card-balance"]')
    const pause = card.get('[data-testid="resilience-field-balancePause"]')
    expect(pause.attributes('min')).toBe('30')
    expect(pause.attributes('max')).toBe('3600')
    expect(card.get('label[for="resilience-balancePause"]').text()).toBe('admin.resilience.settings.fields.balancePause')
    expect(card.get('.input-hint').text()).toBe('admin.resilience.settings.fields.balancePauseHint')
    await w.get('[data-testid="resilience-field-balancePause"]').setValue('600')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.balance.pause_seconds = 600
    expect(mocks.save).toHaveBeenCalledWith(expected)

    await w.get('[data-testid="resilience-field-balancePause"]').setValue('20')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(mocks.save).toHaveBeenCalledTimes(1)
    expect(w.text()).toContain('admin.resilience.settings.invalidNumber')
  })

  it('saves the Claude latency hold switch from its own card', async () => {
    const w = mountSettings(); await flushPromises()
    const card = w.get('[data-testid="resilience-card-anthropicLatencyHold"]')
    expect(card.text()).toContain('admin.resilience.settings.sections.anthropicLatencyHoldHint')
    await card.get('[data-testid="resilience-section-anthropicLatencyHold"]').trigger('click')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.anthropic_latency_hold.enabled = false
    expect(mocks.save).toHaveBeenCalledWith(expected)
  })

  it('saves the same-site hold switch from its own card', async () => {
    const w = mountSettings(); await flushPromises()
    const card = w.get('[data-testid="resilience-card-sameSiteHold"]')
    expect(card.text()).toContain('admin.resilience.settings.sections.sameSiteHoldHint')
    await card.get('[data-testid="resilience-section-sameSiteHold"]').trigger('click')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.same_site_hold.enabled = false
    expect(mocks.save).toHaveBeenCalledWith(expected)
  })

  it('keeps the post-output rebind switch in the session card and greys it out with session handling', async () => {
    const w = mountSettings(); await flushPromises()
    const card = w.get('[data-testid="resilience-card-session"]')
    expect(card.text()).toContain('admin.resilience.settings.switches.postOutputRebindHint')
    const rebind = card.get('[data-testid="resilience-switch-postOutputRebind"]')
    expect(rebind.attributes('disabled')).toBeUndefined()
    await rebind.trigger('click')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.session.post_output_rebind = false
    expect(mocks.save).toHaveBeenCalledWith(expected)

    await card.get('[data-testid="resilience-section-session"]').trigger('click')
    expect(card.get('[data-testid="resilience-switch-postOutputRebind"]').attributes('disabled')).toBeDefined()
  })

  it('saves collaboration plaintext with a trimmed, de-duplicated model list and reloads it', async () => {
    const w = mountSettings(); await flushPromises()
    const card = w.get('[data-testid="resilience-card-collaborationPlaintext"]')
    for (const key of ['Hint', 'Scope', 'Off']) expect(card.text()).toContain(`admin.resilience.settings.sections.collaborationPlaintext${key}`)
    await card.get('[data-testid="resilience-section-collaborationPlaintext"]').trigger('click')
    expect(card.find('[data-testid="resilience-collaborationModels-empty"]').exists()).toBe(true)
    const models = card.get<HTMLTextAreaElement>('[data-testid="resilience-field-collaborationModels"]')
    await models.setValue(' gpt-6.1-sol \n\nGPT-6.1-SOL\ngpt-6-astra\n')
    expect(card.get('[data-testid="resilience-collaborationModels-count"]').text()).toBe('2/16')
    expect(models.element.value).toBe(' gpt-6.1-sol \n\nGPT-6.1-SOL\ngpt-6-astra\n')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.collaboration_plaintext = { enabled: true, models: ['gpt-6.1-sol', 'gpt-6-astra'] }
    expect(mocks.save).toHaveBeenCalledWith(expected)
    expect(models.element.value).toBe('gpt-6.1-sol\ngpt-6-astra')
    expect(card.find('[data-testid="resilience-collaborationModels-empty"]').exists()).toBe(false)
  })

  it('rejects more than 16 or over-long verified models before saving', async () => {
    const w = mountSettings(); await flushPromises()
    const models = w.get('[data-testid="resilience-field-collaborationModels"]')
    await models.setValue(Array.from({ length: 17 }, (_, i) => `model-${i}`).join('\n'))
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('admin.resilience.settings.invalidCollaborationModelsCount')

    await models.setValue('x'.repeat(129))
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(w.text()).toContain('admin.resilience.settings.invalidCollaborationModelLength')
    expect(mocks.save).not.toHaveBeenCalled()

    await models.setValue('x'.repeat(128))
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    expect(mocks.save).toHaveBeenCalledTimes(1)
  })

  it('disables the sections while the master switch is off', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="resilience-enabled"]').trigger('click')
    expect(w.get('[data-testid="resilience-section-budget"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-testid="resilience-section-collaborationPlaintext"]').attributes('disabled')).toBeDefined()
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
    next.collaboration_plaintext.models = ['gpt-6-astra']
    await w.get('[data-testid="resilience-json"]').setValue(JSON.stringify(next))
    await w.get('[data-testid="resilience-apply-json"]').trigger('click')
    expect((w.get('[data-testid="resilience-field-textStall"]').element as HTMLInputElement).value).toBe('90')
    expect((w.get('[data-testid="resilience-field-collaborationModels"]').element as HTMLTextAreaElement).value).toBe('gpt-6-astra')
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

  it('saves the 0.4.4 switches with the rest of the document', async () => {
    const w = mountSettings(); await flushPromises()
    for (const key of ['preludeStaging', 'nativePreludeWhitelist', 'strictTerminal', 'failedFrameRetryAfter']) {
      await w.get(`[data-testid="resilience-switch-${key}"]`).trigger('click')
    }
    await w.get('[data-testid="resilience-field-codexRetryAfterCap"]').setValue('0')
    await w.get('[data-testid="resilience-field-learnTtl"]').setValue('3600')
    await w.get('[data-testid="resilience-section-failure"]').trigger('click')
    await w.get('[data-testid="resilience-section-capability"]').trigger('click')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.stream = { prelude_staging: false, native_prelude_whitelist: false, strict_terminal: false }
    expected.failure.decision_v2 = false
    expected.errors.failed_frame_retry_after = false
    expected.errors.codex_retry_after_cap_seconds = 0
    expected.capability = { stream_only_learning: false, learn_ttl_seconds: 3600 }
    expect(mocks.save).toHaveBeenCalledWith(expected)
  })

  it('places every 0.4.4 switch in its card with a one-line hint', async () => {
    const w = mountSettings(); await flushPromises()
    const placement: Record<string, string[]> = {
      stream: ['preludeStaging', 'nativePreludeWhitelist', 'strictTerminal'],
      errors: ['failedFrameRetryAfter']
    }
    for (const [card, keys] of Object.entries(placement)) {
      const el = w.get(`[data-testid="resilience-card-${card}"]`)
      for (const key of keys) {
        expect(el.find(`[data-testid="resilience-switch-${key}"]`).exists()).toBe(true)
        expect(el.text()).toContain(`admin.resilience.settings.switches.${key}Hint`)
      }
    }
    expect(w.get('[data-testid="resilience-card-errors"]').find('[data-testid="resilience-field-codexRetryAfterCap"]').exists()).toBe(true)
    expect(w.get('[data-testid="resilience-card-capability"]').find('[data-testid="resilience-field-learnTtl"]').exists()).toBe(true)
    expect(w.get('[data-testid="resilience-card-errors"]').text()).toContain('admin.resilience.settings.fields.codexRetryAfterCapHint')
    expect(w.get('[data-testid="resilience-card-capability"]').text()).toContain('admin.resilience.settings.fields.learnTtlHint')
    expect(w.get('[data-testid="resilience-card-failure"]').text()).toContain('admin.resilience.settings.sections.failureHint')
    // The stream card has no switch of its own; the master switch still disables its switches.
    expect(w.find('[data-testid="resilience-section-stream"]').exists()).toBe(false)
    await w.get('[data-testid="resilience-enabled"]').trigger('click')
    expect(w.get('[data-testid="resilience-switch-preludeStaging"]').attributes('disabled')).toBeDefined()
  })

  it('rejects a Codex Retry-After cap or a learning TTL out of range', async () => {
    for (const [field, value] of [['codexRetryAfterCap', '31'], ['learnTtl', '599'], ['learnTtl', '86401']]) {
      mocks.save.mockClear()
      const w = mountSettings(); await flushPromises()
      await w.get(`[data-testid="resilience-field-${field}"]`).setValue(value)
      await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
      expect(mocks.save).not.toHaveBeenCalled()
      expect(w.text()).toContain('admin.resilience.settings.invalidNumber')
    }
  })

  it('greys out the failed-frame switch while the errors card is off', async () => {
    const w = mountSettings(); await flushPromises()
    expect(w.get('[data-testid="resilience-switch-failedFrameRetryAfter"]').attributes('disabled')).toBeUndefined()
    await w.get('[data-testid="resilience-section-errors"]').trigger('click')
    expect(w.get('[data-testid="resilience-switch-failedFrameRetryAfter"]').attributes('disabled')).toBeDefined()
    // The stream switches only follow the master switch.
    expect(w.get('[data-testid="resilience-switch-strictTerminal"]').attributes('disabled')).toBeUndefined()
  })

  it('enables the learning TTL and the Codex cap only with the switch they depend on', async () => {
    const w = mountSettings(); await flushPromises()
    const disabled = (key: string) => w.get(`[data-testid="resilience-field-${key}"]`).attributes('disabled') !== undefined
    expect(disabled('learnTtl')).toBe(false)
    expect(disabled('codexRetryAfterCap')).toBe(false)
    await w.get('[data-testid="resilience-section-capability"]').trigger('click')
    await w.get('[data-testid="resilience-section-errors"]').trigger('click')
    expect(disabled('learnTtl')).toBe(true)
    expect(disabled('codexRetryAfterCap')).toBe(true)
    // Existing fields keep their behaviour.
    expect(disabled('negativeCache')).toBe(false)
  })
  it('renders the fco compat defaults: reactive mode, an empty name list and one hint per mode', async () => {
    const w = mountSettings(); await flushPromises()
    const card = w.get('[data-testid="resilience-card-fcoCompat"]')
    for (const key of ['Hint', 'Scope']) expect(card.text()).toContain(`admin.resilience.settings.sections.fcoCompat${key}`)
    for (const mode of ['off', 'reactive', 'proactive']) {
      expect(card.text()).toContain(`admin.resilience.settings.fcoCompatModes.${mode}Hint`)
    }
    const checked = (mode: string) => card.get<HTMLInputElement>(`[data-testid="resilience-field-fcoCompatMode-${mode}"]`).element.checked
    expect([checked('off'), checked('reactive'), checked('proactive')]).toEqual([false, true, false])
    expect(card.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('0/64')
    expect(card.find('[data-testid="resilience-fcoCompatNames-empty"]').exists()).toBe(true)
    expect(card.get('[data-testid="resilience-fcoCompatName-add"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-testid="resilience-save"]').attributes('disabled')).toBeDefined()
  })

  it('switches the fco compat mode and saves it with the document', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="resilience-field-fcoCompatMode-proactive"]').setValue(true)
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.fco_compat.mode = 'proactive'
    expect(mocks.save).toHaveBeenCalledWith(expected)
    expect(w.get<HTMLInputElement>('[data-testid="resilience-field-fcoCompatMode-proactive"]').element.checked).toBe(true)
  })

  it('greys out the fco compat mode while the master switch is off', async () => {
    const w = mountSettings(); await flushPromises()
    await w.get('[data-testid="resilience-enabled"]').trigger('click')
    for (const mode of ['off', 'reactive', 'proactive']) {
      expect(w.get(`[data-testid="resilience-field-fcoCompatMode-${mode}"]`).attributes('disabled')).toBeDefined()
    }
  })

  it('adds valid fco compat names sorted, refuses invalid, reserved and duplicate ones, and saves the list', async () => {
    const w = mountSettings(); await flushPromises()
    const input = w.get<HTMLInputElement>('[data-testid="resilience-field-fcoCompatName"]')
    const add = w.get('[data-testid="resilience-fcoCompatName-add"]')
    const errorText = () => w.find('[data-testid="resilience-fcoCompatName-error"]')

    for (const name of ['tool_zeta', 'Tool_Alpha', 'tool_beta']) {
      await input.setValue(name)
      await add.trigger('click')
      expect(errorText().exists()).toBe(false)
      expect(input.element.value).toBe('')
    }
    // Enter adds too.
    await input.setValue('x'.repeat(128))
    await input.trigger('keydown', { key: 'Enter' })
    expect(w.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('4/64')

    for (const [name, problem] of [
      [' padded', 'invalid'], ['tool-dash', 'invalid'], ['名字', 'invalid'], ['x'.repeat(129), 'invalid'],
      ['browser_info', 'reserved'], ['codex_apps_selected_page_part_1_of_2', 'reserved'], ['codex_apps_selected_page_part_', 'reserved'],
      ['tool_zeta', 'duplicate']
    ]) {
      await input.setValue(name)
      await add.trigger('click')
      expect(errorText().text()).toBe(`admin.resilience.settings.fcoCompatNameErrors.${problem}`)
      expect(input.element.value).toBe(name)
    }
    // Typing clears the message.
    await input.setValue('tool_new')
    expect(errorText().exists()).toBe(false)

    await w.get('[data-testid="resilience-fcoCompatName-remove-tool_beta"]').trigger('click')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.fco_compat.admin_names = ['Tool_Alpha', 'tool_zeta', 'x'.repeat(128)]
    expect(mocks.save).toHaveBeenCalledWith(expected)
    expect(w.text()).toContain('admin.resilience.settings.saved')
  })

  it('stops adding fco compat names at the cap', async () => {
    const full = settings()
    full.fco_compat.admin_names = Array.from({ length: 64 }, (_, i) => `name_${String(i).padStart(2, '0')}`)
    mocks.get.mockResolvedValueOnce({ settings: full, defaults: settings() })
    const w = mountSettings(); await flushPromises()
    expect(w.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('64/64')
    await w.get('[data-testid="resilience-field-fcoCompatName"]').setValue('one_more')
    await w.get('[data-testid="resilience-fcoCompatName-add"]').trigger('click')
    expect(w.get('[data-testid="resilience-fcoCompatName-error"]').text()).toBe('admin.resilience.settings.fcoCompatNameErrors.full')
    expect(w.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('64/64')
  })

  it('rejects an invalid fco compat document from the advanced JSON before saving', async () => {
    for (const [fco, message] of [
      [{ mode: 'passive', admin_names: [] }, 'invalidFcoCompatMode'],
      [{ mode: 'reactive', admin_names: ['proposed_plan'] }, 'fcoCompatNameErrors.reserved'],
      [{ mode: 'reactive', admin_names: ['bad name'] }, 'fcoCompatNameErrors.invalid'],
      [{ mode: 'reactive', admin_names: ['ok', 7] }, 'fcoCompatNameErrors.invalid'],
      [{ mode: 'reactive', admin_names: Array.from({ length: 65 }, (_, i) => `name_${i}`) }, 'invalidFcoCompatNamesCount'],
      [{ mode: 'reactive', admin_names: 'name' }, 'invalidJson']
    ] as const) {
      mocks.save.mockClear()
      const w = mountSettings(); await flushPromises()
      const next = { ...settings(), stall: { enabled: true, text_stall_seconds: 90 }, fco_compat: fco }
      await w.get('[data-testid="resilience-json"]').setValue(JSON.stringify(next))
      await w.get('[data-testid="resilience-apply-json"]').trigger('click')
      await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
      expect(mocks.save).not.toHaveBeenCalled()
      expect(w.text()).toContain(`admin.resilience.settings.${message}`)
    }
  })

  it('applies an advanced JSON without the fco compat section and keeps the current one', async () => {
    const current = settings()
    current.fco_compat = { mode: 'off', admin_names: ['tool_kept'] }
    mocks.get.mockResolvedValueOnce({ settings: current, defaults: settings() })
    const w = mountSettings(); await flushPromises()
    const legacy: Partial<Settings> = settings()
    delete legacy.fco_compat
    legacy.stall = { enabled: true, text_stall_seconds: 90 }
    await w.get('[data-testid="resilience-json"]').setValue(JSON.stringify(legacy))
    await w.get('[data-testid="resilience-apply-json"]').trigger('click')
    expect(w.text()).not.toContain('admin.resilience.settings.invalidJson')
    expect((w.get('[data-testid="resilience-field-textStall"]').element as HTMLInputElement).value).toBe('90')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.stall.text_stall_seconds = 90
    expected.fco_compat = { mode: 'off', admin_names: ['tool_kept'] }
    expect(mocks.save).toHaveBeenCalledWith(expected)
  })

  it('renders the fco compat defaults when the server answer has no such section', async () => {
    const legacy: Partial<Settings> = settings()
    delete legacy.fco_compat
    const legacyDefaults: Partial<Settings> = settings()
    delete legacyDefaults.fco_compat
    mocks.get.mockResolvedValueOnce({ settings: legacy, defaults: legacyDefaults })
    const w = mountSettings(); await flushPromises()
    expect(w.find('[data-testid="resilience-save"]').exists()).toBe(true)
    expect(w.get<HTMLInputElement>('[data-testid="resilience-field-fcoCompatMode-reactive"]').element.checked).toBe(true)
    expect(w.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('0/64')
    expect(w.get('[data-testid="resilience-save"]').attributes('disabled')).toBeDefined()
  })

  it('counts the de-duplicated names of an applied advanced JSON, as the server stores them', async () => {
    const w = mountSettings(); await flushPromises()
    const next = settings()
    next.fco_compat.admin_names = [...Array.from({ length: 64 }, () => 'a'), 'Zed', 'a']
    await w.get('[data-testid="resilience-json"]').setValue(JSON.stringify(next))
    await w.get('[data-testid="resilience-apply-json"]').trigger('click')
    expect(w.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('2/64')
    await w.get('[data-testid="resilience-field-fcoCompatName"]').setValue('b')
    await w.get('[data-testid="resilience-fcoCompatName-add"]').trigger('click')
    expect(w.find('[data-testid="resilience-fcoCompatName-error"]').exists()).toBe(false)
    expect(w.get('[data-testid="resilience-fcoCompatNames-count"]').text()).toBe('3/64')
    await w.get('[data-testid="resilience-save"]').trigger('click'); await flushPromises()
    const expected = settings()
    expected.fco_compat.admin_names = ['Zed', 'a', 'b']
    expect(mocks.save).toHaveBeenCalledWith(expected)
  })
})

