import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
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
import { identityEntry, m1Response, observabilityEntry, validation } from './fixtures'

const stubs = {
  Toggle: {
    props: ['modelValue', 'disabled'],
    emits: ['update:modelValue'],
    template: '<button type="button" role="switch" :aria-checked="modelValue" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />',
  },
  Select: {
    props: ['modelValue', 'options', 'disabled'],
    emits: ['update:modelValue'],
    template: '<select :value="modelValue" :disabled="disabled" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
  },
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>' },
  Icon: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

function mountPanel(response = m1Response()) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(response),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validation()),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue({
      items: [{ at: '2026-10-23T07:58:40Z', key: 'observability', action: 'admin.cache.policies.observability.update', actor: { user_id: 1, email: 'ops-admin' }, status_code: 200, rev_before: 6, rev_after: 7, changed_fields: ['enabled'], reason: 'open' }],
      history_complete: false,
    }),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-23T08:00:00Z') }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  return { wrapper, api }
}

describe('CacheSettingsPanel', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renders the M1 build: master off, observation editable, later keys announced by milestone', async () => {
    const { wrapper } = mountPanel()
    await flushPromises()
    expect(wrapper.get('[data-test="master-state"]').text()).toContain('admin.cacheSettings.values.off')
    const obs = wrapper.get('[data-test="policy-observability"]')
    expect(obs.findAll('[data-test^="field-observability."]')).toHaveLength(5)
    expect(obs.text()).toContain('ops-admin')
    expect(obs.text()).toContain('gateway.cache_trace.max_byte_budget_mib')
    expect(wrapper.get('[data-test="policy-routing"]').text()).toContain('"milestone":"M4"')
    expect(wrapper.get('[data-test="policy-upstream_identity"] [data-test="policy-not-in-build"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="existing-mechanisms"]').text()).toContain('claude_cache_fallback_policy')
    expect(mocks.leave).toHaveBeenCalled()
  })

  it('shows gated cards as overridden by the master switch, field by field', async () => {
    const { wrapper } = mountPanel(m1Response({ identity: identityEntry() }))
    await flushPromises()
    const card = wrapper.get('[data-test="policy-upstream_identity"]')
    expect(card.attributes('data-state')).toBe('masked_by_master')
    expect(card.get('[data-test="policy-state"]').text()).toContain('admin.cacheSettings.states.masked_by_master')
    expect(card.get('[data-test="master-note"]').text()).toContain('admin.cacheSettings.maskedCard')
    // mode and rollout differ from their defaults, so both are marked; untouched fields are not.
    expect(card.findAll('[data-test="masked-by-master"]')).toHaveLength(2)
    expect(card.find('[data-test="field-upstream_identity.forward_session_headers"] [data-test="masked-by-master"]').exists()).toBe(false)
  })

  it('previews through the server and writes after the low-risk confirmation', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    await wrapper.get('[data-test="field-observability.enabled"] [role="switch"]').trigger('click')
    expect(api.putPolicy).not.toHaveBeenCalled()
    await wrapper.get('[data-test="preview-observability"]').trigger('click')
    await flushPromises()
    expect(api.validatePolicy).toHaveBeenCalledWith('observability', { expected_rev: 7, spec: { enabled: true } })
    expect(wrapper.get('[data-test="diff-enabled"]').text()).toContain('admin.cacheSettings.values.on')
    const saved = observabilityEntry({ stored: { ...observabilityEntry().stored, rev: 8, spec: { ...observabilityEntry().stored.spec, enabled: true } } })
    api.putPolicy.mockResolvedValueOnce(saved)
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0][1]).toMatchObject({ expected_rev: 7, spec: { enabled: true } })
    expect(wrapper.find('[data-test="dialog"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="policy-pending"]').text()).toContain('admin.cacheSettings.written')
    expect(mocks.success).toHaveBeenCalled()
  })

  it('enforces the server confirmation rules for the master switch', async () => {
    const { wrapper, api } = mountPanel()
    api.validatePolicy.mockResolvedValueOnce(validation({
      risk: 'high',
      diff: [{ field: 'enabled', before: false, after: true }],
      confirm: { reason_required: true, checkbox_required: true, type_to_confirm: null },
    }))
    await flushPromises()
    await wrapper.get('[data-test="master-switch"]').trigger('click')
    await flushPromises()
    const submit = () => wrapper.get('[data-test="confirm-submit"]')
    expect(submit().attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="reason"]').setValue('start EXP-1')
    expect(submit().attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    expect(submit().attributes('disabled')).toBeUndefined()
  })

  it('explains a rev conflict and offers a refresh instead of overwriting', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    await wrapper.get('[data-test="field-observability.enabled"] [role="switch"]').trigger('click')
    await wrapper.get('[data-test="preview-observability"]').trigger('click')
    await flushPromises()
    api.putPolicy.mockRejectedValueOnce({ status: 409, code: 'CACHE_POLICY_REV_CONFLICT', metadata: { current_rev: 9 } })
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="submit-failure"]').text()).toContain('admin.cacheSettings.dialog.conflict:{"rev":9}')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="refresh-conflict"]').exists()).toBe(true)
  })

  it('makes every control read-only under the static kill switch', async () => {
    const { wrapper } = mountPanel(m1Response({ kill: true }))
    await flushPromises()
    expect(wrapper.find('[data-test="kill-switch-banner"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="master-switch"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="field-observability.enabled"] [role="switch"]').attributes('disabled')).toBeDefined()
  })

  it('lists the change log with the retention note', async () => {
    const { wrapper, api } = mountPanel()
    await flushPromises()
    const toggle = wrapper.get('[data-test="policy-observability"]').findAll('button').find((b) => b.text().includes('history.toggle'))!
    await toggle.trigger('click')
    await flushPromises()
    expect(api.listHistory).toHaveBeenCalledWith('observability', 20)
    const history = wrapper.get('[data-test="policy-history"]').text()
    expect(history).toContain('ops-admin')
    expect(history).toContain('admin.cacheSettings.history.incomplete')
  })

  it('explains a backend without the endpoints', async () => {
    const { wrapper, api } = mountPanel()
    api.listPolicies.mockReset().mockRejectedValue({ status: 404 })
    const fresh = mount(CacheSettingsPanel, { props: { deps: { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date() } }, global: { stubs } })
    await flushPromises()
    expect(fresh.get('[data-test="settings-load-error"]').text()).toContain('admin.cacheSettings.unavailable')
    wrapper.unmount()
    fresh.unmount()
  })
})

describe('cache settings copy', () => {
  const pick = (locale: typeof zh) => (locale.admin as Record<string, any>).cacheSettings

  it('names every card, state, blocker, warning and error code in both locales', () => {
    const codes = {
      states: ['off', 'on', 'masked_by_master', 'killed', 'blocked', 'pending_reload', 'parse_error', 'not_in_build'],
      blockers: ['diagnostics_disabled', 'static_aware_routing_disabled', 'parse_error', 'kill_switch'],
      warnings: ['observability_off', 'anthropic_observability_missing', 'fallback_group_selected', 'no_effect_rollout_zero', 'high_rollout_without_experiment', 'decision_pending_b4', 'decision_pending_b7', 'decision_pending_b8'],
      errors: ['out_of_range', 'unknown_field', 'requires_mode_uuid_fill', 'not_subset_of_anchor_groups', 'exceeds_static_cap', 'unknown_account', 'unknown_group', 'wrong_platform', 'duplicate', 'unsupported_in_build', 'forbidden_field'],
    }
    for (const locale of [zh, en]) {
      const copy = pick(locale)
      for (const card of POLICY_CARDS) expect(copy.policies[card.key].title).toBeTruthy()
      for (const [group, list] of Object.entries(codes)) for (const code of list) expect(copy[group][code], `${group}.${code}`).toBeTruthy()
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

  it('keeps both locale trees symmetric and free of em and en dashes', () => {
    const keys = (v: unknown, p = ''): string[] =>
      v && typeof v === 'object' ? Object.entries(v).flatMap(([k, c]) => keys(c, p ? `${p}.${k}` : k)) : [p]
    const both = (l: typeof zh) => ({ a: pick(l), b: (l.admin as any).cacheObservability })
    expect(keys(both(zh))).toEqual(keys(both(en)))
    expect(JSON.stringify(both(zh)) + JSON.stringify(both(en))).not.toMatch(/[—–]/)
  })
})
