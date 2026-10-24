import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

const mocks = vi.hoisted(() => ({ success: vi.fn(), leave: vi.fn() }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true,
    locale: { value: 'zh' },
  }),
}))
vi.mock('vue-router', () => ({ onBeforeRouteLeave: mocks.leave }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {} } }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }))

import CacheSettingsPanel from '../CacheSettingsPanel.vue'
import { POLICY_CARDS, fieldCopyKey } from '../catalog'
import type { HistoryResponse, PoliciesResponse, ValidateResult } from '../types'
import type { PoliciesState } from '../usePolicies'
import { history, list, rejection, validation as sampleValidation } from './contract'
import { identityEntry, m1Response, m3Response, observabilityEntry, rewritten, validation, withEntry } from './fixtures'

const stubs = {
  Select: {
    props: ['modelValue', 'options', 'disabled'],
    emits: ['update:modelValue'],
    template: '<select :value="modelValue" :disabled="disabled" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
  },
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>' },
  Icon: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

/** `attach` renders the real BaseDialog into the document, so focus moves the way it does in a browser. */
function mountPanel(response: PoliciesResponse = m1Response(), validate: ValidateResult = validation(), options: { attach?: boolean } = {}) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(response),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue(history()),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-23T09:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, {
    props: { deps },
    global: { stubs: options.attach ? { ...stubs, BaseDialog: false } : stubs },
    attachTo: options.attach ? document.body : undefined,
  })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

/** The real dialog leaves through its transition; focus is checked once it is out of the document, as in a browser. */
async function dialogGone() {
  await flushPromises()
  await vi.waitFor(() => expect(document.querySelector('[data-base-dialog]')).toBeNull())
}

async function previewObservability(wrapper: ReturnType<typeof mountPanel>['wrapper']) {
  await wrapper.get('[data-test="field-observability.enabled"] [role="switch"]').trigger('click')
  await wrapper.get('[data-test="preview-observability"]').trigger('click')
  await flushPromises()
}

describe('CacheSettingsPanel', () => {
  beforeEach(() => vi.clearAllMocks())
  // Attached panels must leave the document between tests, even when an assertion fails.
  enableAutoUnmount(afterEach)

  it('lays out the M1 build: status, passive cards, then the master group; missing keys fold into one line each', async () => {
    const { wrapper } = mountPanel()
    await flushPromises()
    const html = wrapper.html()
    expect(html.indexOf('data-test="instance-status"')).toBeLessThan(html.indexOf('data-test="policy-observability"'))
    expect(html.indexOf('data-test="policy-observability"')).toBeLessThan(html.indexOf('data-test="master-bar"'))
    expect(wrapper.get('[data-test="master-state"]').text()).toContain('admin.cacheSettings.states.off')
    expect(wrapper.get('[data-test="master-loaded"]').text()).toContain('"rev":0')
    const obs = wrapper.get('[data-test="policy-observability"]')
    expect(obs.findAll('[data-test^="field-observability."]')).toHaveLength(5)
    expect(obs.text()).toContain('admin.cacheSettings.footer.neverChanged')
    expect(wrapper.find('[data-test="policy-routing"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="not-included"]').text()).toContain('admin.cacheSettings.policies.northstar.title')
    const gatedMissing = wrapper.get('[data-test="master-bar"] [data-test="gated-not-included"]').text()
    for (const key of ['upstream_identity', 'anthropic_identity', 'routing']) expect(gatedMissing).toContain(`admin.cacheSettings.policies.${key}.title`)
    expect(wrapper.find('[data-test="gated-not-included"] [data-test="policy-risk"]').exists()).toBe(false)
    expect(mocks.leave).toHaveBeenCalled()
  })

  it('nests a gated card under the master switch and says once that the master overrides it', async () => {
    const { wrapper } = mountPanel(m3Response())
    await flushPromises()
    const card = wrapper.get('[data-test="master-bar"] [data-test="policy-upstream_identity"]')
    expect(card.attributes('data-state')).toBe('masked_by_master')
    expect(card.get('[data-test="policy-state"]').text()).toContain('admin.cacheSettings.states.masked_by_master')
    expect(card.html()).not.toContain('masked-by-master')
    // Accounts show their names from the schema candidates.
    expect(card.get('[data-test="control-account_ids"]').text()).toContain('relay-a-01 (#101)')
  })

  it('previews through the server, writes after the low-risk confirmation and re-reads every key', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    await previewObservability(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith('observability', { expected_rev: 0, spec: { enabled: true } })
    expect(wrapper.get('[data-test="diff-enabled"]').text()).toContain('admin.cacheSettings.values.on')
    expect(wrapper.find('[data-test="impact-enabled"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="undo"]').text()).toContain('admin.cacheSettings.undo.plain')
    expect(wrapper.get('[data-test="effective-preview"]').text()).toContain('admin.cacheSettings.states.on')
    api.putPolicy.mockResolvedValueOnce(rewritten(observabilityEntry(), 1, { enabled: true }))
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0][1]).toMatchObject({ expected_rev: 0, spec: { enabled: true } })
    expect(api.listPolicies).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="policy-pending"]').text()).toContain('admin.cacheSettings.written')
    expect(mocks.success.mock.calls[0][0]).toContain('"local"')
  })

  it('announces a countdown only when it starts and when it ends', async () => {
    const { wrapper, api, state } = mountPanel()
    await flushPromises()
    await previewObservability(wrapper)
    api.putPolicy.mockResolvedValueOnce(rewritten(observabilityEntry(), 1, { enabled: true }))
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    const live = () => wrapper.get('[data-test="policy-observability"] [data-test="policy-live"]').text()
    expect(live()).toContain('admin.cacheSettings.writtenLive')
    state.tick(new Date('2026-10-23T09:00:30Z'))
    await flushPromises()
    expect(live()).toContain('admin.cacheSettings.writtenLive')
    state.tick(new Date('2026-10-23T09:01:01Z'))
    await flushPromises()
    expect(live()).toContain('admin.cacheSettings.settledLive')
    expect(api.listPolicies).toHaveBeenCalledTimes(3)
  })

  it('lists the gated keys with their stored configuration and the states the server reports for the master', async () => {
    const { wrapper } = mountPanel(m3Response(), sampleValidation('validate_ok_master_enable_m3'))
    await flushPromises()
    await wrapper.get('[data-test="master-switch"]').trigger('click')
    await flushPromises()
    const row = wrapper.get('[data-test="affected-upstream_identity"]').text()
    expect(row).toContain('admin.cacheSettings.states.masked_by_master')
    expect(row).toContain('admin.cacheSettings.states.on')
    expect(row).toContain('admin.cacheSettings.dialog.idCount')
    expect(wrapper.get('[data-test="after-submit"]').text()).toContain('admin.cacheSettings.dialog.masterOnLead')
    const submit = () => wrapper.get('[data-test="confirm-submit"]')
    expect(submit().attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    // The disabled button says what is still missing.
    expect(submit().attributes('aria-describedby')).toBe('cache-policy-submit-hint')
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.reason')
    await wrapper.get('[data-test="reason"]').setValue('开始试运行')
    expect(submit().attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="submit-hint"]').text()).not.toContain('admin.cacheSettings.missing.reason')
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.checkbox')
    expect(wrapper.get('[data-test="confirm-check"]').text()).toContain('admin.cacheSettings.dialog.checkboxScope')
    expect(wrapper.get('[data-test="confirm-check"]').classes().join(' ')).not.toMatch(/bg-red/)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    expect(submit().attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="submit-hint"]').exists()).toBe(false)
  })

  it('keeps the master write receipt visible in the master group', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    await wrapper.get('[data-test="master-switch"]').trigger('click')
    await flushPromises()
    const master = list('list_m3_master_on').policies.find((p) => p.key === 'master')!
    api.putPolicy.mockResolvedValueOnce(master)
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    // M1 has no gated card, so the first receipt inside the master group is the group's own.
    const group = wrapper.get('[data-test="master-bar"]')
    expect(group.get('[data-test="policy-pending"]').text()).toContain('admin.cacheSettings.written')
    expect(group.get('[data-test="policy-live"]').text()).toContain('admin.cacheSettings.writtenLive')
  })

  it('hands focus back to the master switch after its confirmation closes, though it was disabled meanwhile (m-new-1)', async () => {
    const { wrapper } = mountPanel(m1Response(), validation(), { attach: true })
    await flushPromises()
    const masterSwitch = wrapper.get<HTMLButtonElement>('[data-test="master-switch"]').element
    masterSwitch.focus()
    masterSwitch.click()
    await flushPromises()
    expect(document.querySelector('[data-base-dialog]')).not.toBeNull()
    expect(masterSwitch.disabled).toBe(true)
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true }))
    await dialogGone()
    expect(document.activeElement).not.toBe(document.body)
    expect(document.activeElement).toBe(masterSwitch)
  })

  it('hands focus back to Preview after a cancel, and to the card title once the saved draft disables it (m-new-1)', async () => {
    const { wrapper, api } = mountPanel(m1Response(), validation(), { attach: true })
    await flushPromises()
    await wrapper.get('[data-test="field-observability.enabled"] [role="switch"]').trigger('click')
    const preview = wrapper.get<HTMLButtonElement>('[data-test="preview-observability"]').element
    const open = async () => {
      preview.focus()
      preview.click()
      await flushPromises()
      expect(document.activeElement?.closest('[data-base-dialog]')).not.toBeNull()
    }
    await open()
    document.querySelector<HTMLButtonElement>('[data-test="confirm-cancel"]')!.click()
    await dialogGone()
    expect(document.activeElement).toBe(preview)

    await open()
    api.putPolicy.mockResolvedValueOnce(rewritten(observabilityEntry(), 1, { enabled: true }))
    document.querySelector<HTMLButtonElement>('[data-test="confirm-submit"]')!.click()
    await dialogGone()
    expect(preview.disabled).toBe(true)
    expect(document.activeElement).not.toBe(document.body)
    expect(document.activeElement?.id).toBe('cache-policy-observability')
  })

  it('marks a card with unsubmitted edits and states what a clipped value runs at', async () => {
    const { wrapper } = mountPanel(list('list_clipped_static_cap'))
    await flushPromises()
    const card = wrapper.get('[data-test="policy-observability"]')
    expect(card.find('[data-test="policy-unsaved"]').exists()).toBe(false)
    expect(card.get('[data-test="field-observability.byte_budget_mib"]').text()).toContain('admin.cacheSettings.clippedRunsAt')
    expect(card.get('[data-test="field-observability.byte_budget_mib"]').text()).toContain('32 MiB')
    await card.get('[data-test="field-observability.enabled"] [role="switch"]').trigger('click')
    expect(card.get('[data-test="policy-unsaved"]').text()).toContain('"n":1')
  })

  it('binds the checkbox to the warnings and requires the exact key for the highest risk', async () => {
    const { wrapper } = mountPanel(m1Response(), sampleValidation('validate_ok_routing_enable_m4'))
    await flushPromises()
    await previewObservability(wrapper)
    expect(wrapper.get('[data-test="confirm-check"]').text()).toContain('admin.cacheSettings.dialog.checkboxItems')
    expect(wrapper.get('[data-test="checklist"]').text()).toContain('admin.cacheSettings.acks.decision_pending_static_routing')
    expect(wrapper.get('[data-test="checklist"]').text()).toContain('admin.cacheSettings.acks.static_aware_routing_disabled')
    await wrapper.get('[data-test="reason"]').setValue('small share')
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing_policy')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('turns off in one step with a neutral risk badge', async () => {
    const { wrapper } = mountPanel(withEntry(m1Response(), rewritten(observabilityEntry(), 3, { enabled: true })), sampleValidation('validate_ok_disable'))
    await flushPromises()
    await previewObservability(wrapper)
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-gray')
    expect(wrapper.get('[data-test="dialog-risk"]').text()).toContain('admin.cacheSettings.dialog.riskDisable')
    expect(wrapper.find('[data-test="confirm-check"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="confirm-submit"]').classes()).toContain('btn-primary')
  })

  it('makes a 409 a three-way merge that waits for each clashing field and keeps the typed reason', async () => {
    const { wrapper, api } = mountPanel(m3Response(), { ...sampleValidation('validate_rev_stale'), current_rev: 4, rev_stale: false })
    await flushPromises()
    const card = wrapper.get('[data-test="policy-upstream_identity"]')
    await card.get('[data-test="field-upstream_identity.rollout_percent"] input').setValue('60')
    await card.get('[data-test="field-upstream_identity.rollout_percent"] input').trigger('change')
    await wrapper.get('[data-test="preview-upstream_identity"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="reason"]').setValue('扩大到 60%')
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    api.putPolicy.mockRejectedValueOnce(rejection('put_409'))
    api.getPolicy.mockResolvedValueOnce(rewritten(identityEntry(), 5, { rollout_percent: 0 }, 'oncall@example.com', '错误率上升，先调回 0'))
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="conflict-writes"]').text()).toContain('oncall@example.com')
    expect(wrapper.get('[data-test="overlap-rollout_percent"] [data-test="overlap-reverting"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="resolve-conflict"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="choose-mine-rollout_percent"]').trigger('change')
    expect(wrapper.get('[data-test="resolve-conflict"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-test="resolve-conflict"]').trigger('click')
    await flushPromises()
    expect(api.validatePolicy).toHaveBeenLastCalledWith('upstream_identity', { expected_rev: 5, spec: { rollout_percent: 60 } })
    expect((wrapper.get('[data-test="reason"]').element as HTMLTextAreaElement).value).toBe('扩大到 60%')
    expect((wrapper.get('[data-test="confirm-checkbox"]').element as HTMLInputElement).checked).toBe(false)
  })

  it('keeps every card editable under the kill switch and says the write only saves', async () => {
    const { wrapper } = mountPanel(m1Response({ kill: true }))
    await flushPromises()
    expect(wrapper.find('[data-test="kill-switch-banner"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="master-switch"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="policy-observability"]').attributes('data-state')).toBe('killed')
    await previewObservability(wrapper)
    expect(wrapper.find('[data-test="kill-note"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="confirm-submit"]').text()).toContain('admin.cacheSettings.dialog.submitSaveOnly')
  })

  it('explains why preview is unavailable and jumps to the first error', async () => {
    const { wrapper } = mountPanel()
    await flushPromises()
    const input = wrapper.get('[data-test="field-observability.sample_percent"] input')
    await input.setValue('150')
    await input.trigger('change')
    expect(wrapper.get('[data-test="preview-observability"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="preview-observability"]').attributes('aria-describedby')).toBe('cache-policy-blocked-observability')
    expect(wrapper.get('[data-test="preview-blocked"]').text()).toContain('"n":1')
    expect(wrapper.get('[data-test="field-observability.sample_percent"]').text()).toContain('"min":0')
  })

  it('lists failed writes in the change log at full contrast', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    const toggle = wrapper.get('[data-test="policy-observability"]').findAll('button').find((b) => b.text().includes('history.toggle'))!
    await toggle.trigger('click')
    await flushPromises()
    expect(api.listHistory).toHaveBeenCalledWith('observability', 20)
    const failed = wrapper.get('[data-test="policy-history"] [data-failed]')
    expect(failed.text()).toContain('admin.cacheSettings.history.failedWrite')
    expect(failed.text()).toContain('admin.cacheSettings.history.status.422')
    expect(failed.classes().join(' ')).not.toMatch(/text-gray-4/)
    // Field paths from the audit log are shown with the card's labels.
    const written = wrapper.findAll('[data-test="policy-history"] li')[0].text()
    expect(written).toContain('admin.cacheSettings.history.wrote')
    expect(written).toContain('admin.cacheSettings.fields.observability.rollout_percent.label')
  })

  it('says why the change log is incomplete and offers a refresh only for a gap (§7.6)', async () => {
    const legacy = { ...history('history_truncated_retention'), history_incomplete_reason: undefined } as unknown as HistoryResponse
    const cases: [string, HistoryResponse, string | null][] = [
      ['history_truncated_retention', history('history_truncated_retention'), 'earlier_missing'],
      ['history_empty_outside_window', history('history_empty_outside_window'), 'earlier_missing'],
      ['history_gap', history('history_gap'), 'gap'],
      ['history_pending_flush', history('history_pending_flush'), null],
      ['history_empty_within_window', history('history_empty_within_window'), null],
      // A backend before v1.7.4.6 sends no reason; its only cause was the retention period.
      ['no reason', legacy, 'earlier_missing'],
    ]
    for (const [name, response, reason] of cases) {
      const { wrapper, api } = mountPanel()
      api.listHistory.mockResolvedValue(response)
      await flushPromises()
      await wrapper.get('[data-test="policy-observability"]').findAll('button').find((b) => b.text().includes('history.toggle'))!.trigger('click')
      await flushPromises()
      const note = wrapper.find('[data-test="history-incomplete"]')
      expect(note.exists(), name).toBe(reason !== null)
      if (!reason) continue
      expect(note.text(), name).toContain(`admin.cacheSettings.history.incomplete.${reason}`)
      const refresh = wrapper.find('[data-test="history-refresh"]')
      expect(refresh.exists(), name).toBe(reason === 'gap')
      if (reason !== 'gap') continue
      await refresh.trigger('click')
      expect(api.listHistory, name).toHaveBeenCalledTimes(2)
    }
  })

  it('keeps focus in the change log when a retry or a gap refresh swaps its content', async () => {
    const { wrapper, api } = mountPanel(m1Response(), validation(), { attach: true })
    api.listHistory.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(history('history_gap'))
    await flushPromises()
    await wrapper.get('[data-test="policy-observability"]').findAll('button').find((b) => b.text().includes('history.toggle'))!.trigger('click')
    await flushPromises()
    const log = wrapper.get<HTMLElement>('[data-test="policy-history"]').element
    // Failed load, then a log with a gap: each button is replaced by the loading line when clicked.
    for (const name of ['admin.cacheSettings.retry', 'admin.cacheSettings.history.refresh']) {
      const button = wrapper.get('[data-test="policy-history"]').findAll('button').find((b) => b.text() === name)!.element as HTMLButtonElement
      button.focus()
      button.click()
      await flushPromises()
      expect(document.activeElement, name).not.toBe(document.body)
      expect(document.activeElement, name).toBe(log)
    }
    expect(api.listHistory).toHaveBeenCalledTimes(3)
    expect(wrapper.find('[data-test="history-refresh"]').exists()).toBe(true)
    // The focused log is named, so a screen reader says where focus went.
    expect(log.getAttribute('aria-label')).toBe('admin.cacheSettings.history.toggle')
  })

  it('links to the gateway tab, names the place with the settings page keys and follows only in-app links (C-14)', async () => {
    const { wrapper } = mountPanel()
    await flushPromises()
    // Only the link value is checked here; that the gateway tab opens is checked in the browser (R, S).
    const sampleLink = m1Response().existing.claude_cache_fallback!.settings_link!
    expect(sampleLink.startsWith('/admin/')).toBe(true)
    expect(new URLSearchParams(sampleLink.split('?')[1] ?? '').get('tab')).toBe('gateway')
    const links = wrapper.findAll('[data-test="existing-mechanisms"] a')
    expect(links.map((l) => l.attributes('href'))).toEqual([sampleLink, '/admin/settings?tab=gateway'])
    // The place is spelled from the settings page's own keys, so a rename there carries over (§11.7).
    for (const [link, section] of [
      [links[0], 'admin.settings.claudeCacheFallback.title'],
      [links[1], 'admin.resilience.settings.title'],
    ] as const) {
      expect(link.text()).toContain('admin.cacheSettings.existing.editAt')
      expect(link.text()).toContain('"page":"admin.settings.title"')
      expect(link.text()).toContain('"tab":"admin.settings.tabs.gateway"')
      expect(link.text()).toContain(`"section":"${section}"`)
    }
    expect(wrapper.get('[data-test="existing-mechanisms"]').text()).toContain('admin.cacheSettings.existing.staticNote')

    for (const outsideLink of ['https://example.com/admin/', '//example.com/admin/', 'javascript:alert(1)']) {
      const outside = m1Response()
      outside.existing.claude_cache_fallback = { configured: true, group_count: 2, settings_link: outsideLink }
      const other = mountPanel(outside)
      await flushPromises()
      expect(other.wrapper.get('[data-test="existing-mechanisms"] a').attributes('href'), outsideLink).toBe('/admin/settings')
    }
  })

  it('asks in an in-app dialog before discarding drafts on reload', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    await wrapper.get('[data-test="field-observability.enabled"] [role="switch"]').trigger('click')
    const reload = wrapper.findAll('button').find((b) => b.text().includes('admin.cacheSettings.reload'))!
    await reload.trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="dialog"]').text()).toContain('admin.cacheSettings.leave.message')
    expect(api.listPolicies).toHaveBeenCalledTimes(1)
  })

  it('explains a backend without the endpoints', async () => {
    const { api } = mountPanel()
    api.listPolicies.mockReset().mockRejectedValue(rejection('put_404'))
    const fresh = mount(CacheSettingsPanel, { props: { deps: { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date() } }, global: { stubs } })
    await flushPromises()
    expect(fresh.get('[data-test="settings-load-error"]').text()).toContain('admin.cacheSettings.unavailable')
  })
})

describe('cache settings copy', () => {
  const pick = (locale: typeof zh) => (locale.admin as Record<string, any>).cacheSettings

  it('names every card, state, blocker, warning, error, missing confirmation and option in both locales', () => {
    const codes = {
      states: ['off', 'on', 'masked_by_master', 'killed', 'blocked', 'pending_reload', 'parse_error', 'not_in_build'],
      blockers: ['diagnostics_disabled', 'static_aware_routing_disabled', 'parse_error', 'kill_switch'],
      warnings: [
        'observability_off',
        'anthropic_observability_missing',
        'fallback_group_selected',
        'no_effect_rollout_zero',
        'high_rollout_without_experiment',
        'decision_pending_header_policy',
        'decision_pending_fallback_scope',
        'decision_pending_static_routing',
      ],
      errors: [
        'out_of_range',
        'unknown_field',
        'requires_mode_uuid_fill',
        'not_subset_of_anchor_groups',
        'exceeds_static_cap',
        'unknown_account',
        'unknown_group',
        'wrong_platform',
        'duplicate',
        'unsupported_in_build',
        'forbidden_field',
        'too_long',
        'malformed_body',
      ],
      missing: ['reason', 'checkbox', 'typed'],
      // Enum values by i18n key ('+' becomes '_plus_'); affinity_mode uses the DESIGN v1.7.6 names.
      options: ['off', 'uuid_fill', 'whitelist', 'normalize', 'score', 'prev_host', 'prev_host_plus_quality', 'aux_only', 'openai', 'anthropic', 'gemini', 'messages_bridge'],
    }
    const routingValues = sampleValidation('validate_ok_routing_enable_m4').diff.filter((d) => d.field === 'affinity_mode').flatMap((d) => [d.before, d.after])
    expect(routingValues).toContain('prev_host')
    for (const locale of [zh, en]) {
      const copy = pick(locale)
      for (const card of POLICY_CARDS) expect(copy.policies[card.key].title).toBeTruthy()
      for (const [group, list] of Object.entries(codes)) for (const code of list) expect(copy[group][code], `${group}.${code}`).toBeTruthy()
      for (const code of [...codes.warnings, ...codes.blockers]) expect(copy.acks[code], `acks.${code}`).toBeTruthy()
      for (const value of routingValues) expect(copy.options[String(value).replace(/\+/g, '_plus_')], `options.${value}`).toBeTruthy()
      for (const reason of ['earlier_missing', 'gap']) expect(copy.history.incomplete[reason], `history.incomplete.${reason}`).toBeTruthy()
      expect(copy.history.refresh).toBeTruthy()
      // The names before v1.7.6 are gone, not kept beside the new ones.
      for (const old of ['host', 'host_plus_score']) expect(copy.options[old], `options.${old}`).toBeUndefined()
    }
  })

  it('has label, description and impact for every DESIGN §11.3 field', () => {
    const fields: Record<string, string[]> = {
      master: ['enabled'],
      observability: ['enabled', 'sample_percent', 'chain_percent', 'byte_budget_mib', 'rate_mib_per_s', 'platforms', 'ingress_chain_percent', 'persist_trace_hourly'],
      northstar: ['rollup_enabled', 'backfill_days', 'policy_version', 'excluded_group_ids', 'exclude_media', 'unknown_threshold', 'model_thresholds'],
      upstream_identity: ['mode', 'account_ids', 'rollout_percent', 'chat_compat_uuid', 'header_only_uuid', 'forward_session_headers'],
      anthropic_identity: ['anchor_group_ids', 'gemini_anchor', 'metadata_group_ids', 'rollout_percent'],
      routing: ['keep_sticky_on_busy.enabled', 'keep_sticky_on_busy.group_ids', 'keep_sticky_on_busy.rollout_percent', 'affinity_mode'],
    }
    const lookup = (root: unknown, path: string) => path.split('.').reduce<any>((node, part) => node?.[part], root)
    for (const locale of [zh, en]) {
      for (const [key, list] of Object.entries(fields)) {
        for (const field of list) {
          for (const part of ['label', 'description', 'risk'] as const) {
            expect(lookup(locale, fieldCopyKey(key, field, part)), `${key}.${field}.${part}`).toBeTruthy()
          }
        }
      }
    }
  })

  it('keeps both locale trees symmetric, free of em and en dashes and of internal codes (m-2)', () => {
    const keys = (v: unknown, p = ''): string[] =>
      v && typeof v === 'object' ? Object.entries(v).flatMap(([k, c]) => keys(c, p ? `${p}.${k}` : k)) : [p]
    const both = (l: typeof zh) => ({ a: pick(l), b: (l.admin as any).cacheObservability })
    expect(keys(both(zh))).toEqual(keys(both(en)))
    const text = JSON.stringify(both(zh)) + JSON.stringify(both(en))
    expect(text).not.toMatch(/[—–]/)
    expect(text).not.toMatch(/\b(?:EXP-\d|[BLM]-?\d{1,2}\b|P0[ab]|T0[ab]|INV-\d)/)
  })

  it('states performance numbers only as design targets (m-1)', () => {
    for (const locale of [zh, en]) {
      const fields = pick(locale).fields.observability
      expect(fields.enabled.risk).toMatch(/设计目标|Design target/)
      expect(fields.rate_mib_per_s.risk).toMatch(/不丢弃|not dropped/)
    }
  })
})
