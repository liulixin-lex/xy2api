// M4: cache_anthropic_identity_policy (risk high) and cache_routing_policy (risk highest) on the
// generic policy card, driven by F's m3m4 samples (v1.0.2, M4 part). The backend (T4.0) is not
// built yet, so the samples follow DESIGN §11 and the M4 brief: group candidates (FE-Q3), dot
// paths for the nested routing fields, and the key name typed before routing widens (§11.7).
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

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
import { toWriteFailure } from '../api'
import type { PoliciesResponse, PolicySpec, ValidateResult } from '../types'
import type { PoliciesState } from '../usePolicies'
import { M3M4_NAMES, m3m4Body, m3m4Draft, m3m4History, m3m4List, m3m4Rejection, m3m4Validation, m3m4Written } from './m3m4'

const ANTHROPIC = 'anthropic_identity'
const ROUTING = 'routing'

/** The admin group list: one OpenAI group the M4 fields must not offer, and 77 as the fallback of 31 (C-13). */
const GROUPS = [
  { id: 18, name: 'openai-pool', platform: 'openai' },
  { id: 31, name: 'claude-pool-a', platform: 'anthropic', availability_fallback_group_id: 77 },
  { id: 32, name: 'claude-pool-b', platform: 'anthropic' },
  { id: 77, name: 'claude-standby', platform: 'anthropic' },
]

function entry(response: PoliciesResponse, key: string) {
  const found = response.policies.find((p) => p.key === key)
  if (!found) throw new Error(`no ${key}`)
  return found
}

const stubs = {
  // Native select that hands back the option's own value, so group ids stay numbers.
  Select: {
    props: ['modelValue', 'options', 'disabled'],
    emits: ['update:modelValue'],
    template:
      '<select :value="modelValue" :disabled="disabled" @change="$emit(\'update:modelValue\', options.find((o) => String(o.value) === $event.target.value)?.value)"><option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option></select>',
  },
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>' },
  Icon: true,
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
}

function mountPanel(list: string | PoliciesResponse, options: { validate?: ValidateResult; groups?: 'failed' } = {}) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(typeof list === 'string' ? m3m4List(list) : list),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(options.validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn((key: string) => Promise.resolve(m3m4History(key === ROUTING ? 'm4_history_routing' : 'm4_history_anthropic'))),
  }
  const loadGroups = options.groups === 'failed' ? vi.fn().mockRejectedValue(new Error('groups')) : vi.fn().mockResolvedValue(structuredClone(GROUPS))
  const deps = { api, loadGroups, now: () => new Date('2026-10-24T09:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']

const field = (wrapper: Wrapper, key: string, name: string) => wrapper.get(`[data-test="field-${key}.${name}"]`)
const picker = (wrapper: Wrapper, key: string, name: string) => field(wrapper, key, name).get('select')
const offered = (wrapper: Wrapper, key: string, name: string) => picker(wrapper, key, name).findAll('option').map((o) => o.attributes('value'))

async function pick(wrapper: Wrapper, key: string, name: string, ...ids: number[]) {
  for (const id of ids) await picker(wrapper, key, name).setValue(String(id))
}

async function setNumber(wrapper: Wrapper, key: string, name: string, value: string) {
  const input = field(wrapper, key, name).get('input')
  await input.setValue(value)
  await input.trigger('change')
}

async function preview(wrapper: Wrapper, key: string) {
  await wrapper.get(`[data-test="preview-${key}"]`).trigger('click')
  await flushPromises()
}

const submit = (wrapper: Wrapper) => wrapper.get('[data-test="confirm-submit"]')

describe('anthropic_identity and routing (M4) on the generic policy card, m3m4 samples', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the M4 samples the cards use', () => {
    expect(M3M4_NAMES.filter((n) => n.startsWith('m4_'))).toHaveLength(18)
  })

  it('renders the identity card under the master switch: high risk, the observability warning, groups by name (m4_list_masked_by_master)', async () => {
    const { wrapper } = mountPanel('m4_list_masked_by_master')
    await flushPromises()
    const card = wrapper.get(`[data-test="master-bar"] [data-test="policy-${ANTHROPIC}"]`)
    for (const f of entry(m3m4List('m4_list_masked_by_master'), ANTHROPIC).schema) {
      expect(card.find(`[data-test="field-${ANTHROPIC}.${f.field}"]`).exists(), f.field).toBe(true)
    }
    expect(card.get('[data-test="policy-risk"]').text()).toContain('admin.cacheSettings.risk.high')
    expect(card.get('[data-test="policy-warnings"]').text()).toContain('admin.cacheSettings.warnings.anthropic_observability_missing')
    expect(field(wrapper, ANTHROPIC, 'anchor_group_ids').text()).toContain('claude-pool-a (#31)')
    expect(field(wrapper, ANTHROPIC, 'anchor_group_ids').text()).toContain('claude-pool-b (#32)')
    expect(field(wrapper, ANTHROPIC, 'metadata_group_ids').text()).toContain('claude-pool-a (#31)')
  })

  it('offers only the groups the server lists (FE-Q3), marks the fallback group, and needs no admin group list for that', async () => {
    const listed = mountPanel('m4_list_default')
    await flushPromises()
    expect(offered(listed.wrapper, ANTHROPIC, 'anchor_group_ids')).toEqual(['31', '32', '77'])
    expect(picker(listed.wrapper, ANTHROPIC, 'anchor_group_ids').find('option[value="77"]').text()).toContain('admin.cacheSettings.groupPicker.fallbackTag')
    expect(offered(listed.wrapper, ROUTING, 'keep_sticky_on_busy.group_ids')).toEqual(['31', '32', '77'])

    // The admin group list failed: the candidates still name and offer the groups; only the fallback mark is missing.
    const failed = mountPanel('m4_list_default', { groups: 'failed' })
    await flushPromises()
    const anchors = field(failed.wrapper, ANTHROPIC, 'anchor_group_ids')
    expect(anchors.get('select').attributes('disabled')).toBeUndefined()
    expect(offered(failed.wrapper, ANTHROPIC, 'anchor_group_ids')).toEqual(['31', '32', '77'])
    expect(anchors.text()).toContain('admin.cacheSettings.groupPicker.fallbackUnknown')
    await pick(failed.wrapper, ANTHROPIC, 'anchor_group_ids', 32)
    expect(anchors.text()).toContain('claude-pool-b (#32)')

    // A backend without candidates falls back to the admin group list, as in M1.
    const bare = m3m4List('m4_list_default')
    for (const f of entry(bare, ANTHROPIC).schema) delete f.options
    const unlisted = mountPanel(bare, { groups: 'failed' })
    await flushPromises()
    expect(picker(unlisted.wrapper, ANTHROPIC, 'anchor_group_ids').attributes('disabled')).toBeDefined()
    expect(field(unlisted.wrapper, ANTHROPIC, 'anchor_group_ids').text()).toContain('admin.cacheSettings.groupPicker.loadFailed')
  })

  it('asks for a reason and a check before widening the identity, names the warning, and writes the sample body (m4_validate_anthropic_enable)', async () => {
    const { wrapper, api } = mountPanel('m4_list_default', { validate: m3m4Validation('m4_validate_anthropic_enable') })
    await flushPromises()
    await pick(wrapper, ANTHROPIC, 'anchor_group_ids', 31, 32)
    await pick(wrapper, ANTHROPIC, 'metadata_group_ids', 31)
    await setNumber(wrapper, ANTHROPIC, 'rollout_percent', '50')
    await preview(wrapper, ANTHROPIC)
    expect(api.validatePolicy).toHaveBeenCalledWith(ANTHROPIC, m3m4Draft('m4_validate_anthropic_enable'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.enable')
    expect(wrapper.get('[data-test="validate-warnings"]').text()).toContain('admin.cacheSettings.warnings.anthropic_observability_missing')
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="reason"]').setValue(m3m4Body('m4_validate_anthropic_enable').reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    expect(submit(wrapper).attributes('disabled')).toBeUndefined()
    api.putPolicy.mockResolvedValueOnce(m3m4Written('m4_put_anthropic_200'))
    await submit(wrapper).trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([ANTHROPIC, m3m4Body('m4_validate_anthropic_enable')])
  })

  it('keeps the metadata groups within the anchor groups before the preview, the rule of m4_validate_anthropic_not_subset', async () => {
    expect(m3m4Validation('m4_validate_anthropic_not_subset').errors).toEqual([{ field: 'metadata_group_ids', code: 'not_subset_of_anchor_groups' }])
    const { wrapper, api } = mountPanel('m4_list_default')
    await flushPromises()
    await pick(wrapper, ANTHROPIC, 'anchor_group_ids', 31)
    await pick(wrapper, ANTHROPIC, 'metadata_group_ids', 31, 32)
    expect(field(wrapper, ANTHROPIC, 'metadata_group_ids').text()).toContain('admin.cacheSettings.errors.not_subset_of_anchor_groups')
    expect(wrapper.get(`[data-test="preview-${ANTHROPIC}"]`).attributes('disabled')).toBeDefined()
    expect(api.validatePolicy).not.toHaveBeenCalled()
  })

  it('marks a fallback group as it is added and carries the warning (m4_validate_anthropic_fallback_warning)', async () => {
    const { wrapper, api } = mountPanel('m4_list_on', { validate: m3m4Validation('m4_validate_anthropic_fallback_warning') })
    await flushPromises()
    await pick(wrapper, ANTHROPIC, 'anchor_group_ids', 77)
    const chip = field(wrapper, ANTHROPIC, 'anchor_group_ids').findAll('li').find((li) => li.text().includes('#77'))
    expect(chip?.text()).toContain('admin.cacheSettings.groupPicker.fallbackTag')
    await preview(wrapper, ANTHROPIC)
    expect(api.validatePolicy).toHaveBeenCalledWith(ANTHROPIC, m3m4Draft('m4_validate_anthropic_fallback_warning'))
    expect(wrapper.get('[data-test="validate-warnings"]').text()).toContain('admin.cacheSettings.warnings.fallback_group_selected')
  })

  it('renders the routing card from its dot paths, with the highest risk tag (m4_list_masked_by_master)', async () => {
    const { wrapper } = mountPanel('m4_list_masked_by_master')
    await flushPromises()
    const card = wrapper.get(`[data-test="master-bar"] [data-test="policy-${ROUTING}"]`)
    expect(card.get('[data-test="policy-risk"]').text()).toContain('admin.cacheSettings.risk.highest')
    expect(field(wrapper, ROUTING, 'keep_sticky_on_busy.enabled').get('[role="switch"]').attributes('aria-checked')).toBe('true')
    expect(field(wrapper, ROUTING, 'keep_sticky_on_busy.group_ids').text()).toContain('claude-pool-a (#31)')
    expect((field(wrapper, ROUTING, 'keep_sticky_on_busy.rollout_percent').get('input').element as HTMLInputElement).value).toBe('20')
    expect(offered(wrapper, ROUTING, 'affinity_mode')).toEqual(['score', 'prev_host', 'prev_host+quality'])
    expect((picker(wrapper, ROUTING, 'affinity_mode').element as HTMLSelectElement).value).toBe('score')
    // 18 is the OpenAI group of m4_validate_routing_wrong_platform: never offered.
    expect(offered(wrapper, ROUTING, 'keep_sticky_on_busy.group_ids')).toEqual(['32', '77'])
  })

  it('asks for the key name before routing widens, and writes it (highest, m4_validate_routing_enable)', async () => {
    const { wrapper, api } = mountPanel('m4_list_default', { validate: m3m4Validation('m4_validate_routing_enable') })
    await flushPromises()
    await field(wrapper, ROUTING, 'keep_sticky_on_busy.enabled').get('[role="switch"]').trigger('click')
    await setNumber(wrapper, ROUTING, 'keep_sticky_on_busy.rollout_percent', '20')
    await preview(wrapper, ROUTING)
    const sample = m3m4Body('m4_validate_routing_enable')
    const [key, sent] = api.validatePolicy.mock.calls[0]
    expect(key).toBe(ROUTING)
    // The nested object goes whole (partialSpec), so the untouched group list rides along at its stored value.
    expect(sent).toEqual({ expected_rev: sample.expected_rev, spec: { keep_sticky_on_busy: { group_ids: [], ...(sample.spec.keep_sticky_on_busy as PolicySpec) } } })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="dialog-risk"]').text()).toContain('admin.cacheSettings.risk.highest')
    expect(wrapper.get('[data-test="dialog"]').text()).toContain('admin.cacheSettings.dialog.typeToConfirm:{"key":"cache_routing_policy"}')
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.typed')
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing')
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing_policy')
    expect(submit(wrapper).attributes('disabled')).toBeUndefined()
    api.putPolicy.mockResolvedValueOnce(m3m4Written('m4_put_routing_200'))
    await submit(wrapper).trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([ROUTING, { ...sample, spec: sent.spec }])
  })

  it('asks for the key name when the PUT says it is missing, then sends it (m4_put_routing_422_typed)', async () => {
    const older = { ...m3m4Validation('m4_validate_routing_enable'), confirm: { reason_required: true, checkbox_required: true, type_to_confirm: null } }
    const { wrapper, api } = mountPanel('m4_list_default', { validate: older })
    await flushPromises()
    await field(wrapper, ROUTING, 'keep_sticky_on_busy.enabled').get('[role="switch"]').trigger('click')
    await setNumber(wrapper, ROUTING, 'keep_sticky_on_busy.rollout_percent', '20')
    await preview(wrapper, ROUTING)
    const sample = m3m4Body('m4_put_routing_422_typed')
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    api.putPolicy.mockRejectedValueOnce(m3m4Rejection('m4_put_routing_422_typed')).mockResolvedValueOnce(m3m4Written('m4_put_routing_200'))
    await submit(wrapper).trigger('click')
    await flushPromises()
    const first = api.putPolicy.mock.calls[0][1]
    expect(first).toEqual({ ...sample, spec: first.spec })
    expect(first.spec.keep_sticky_on_busy).toMatchObject(sample.spec.keep_sticky_on_busy as PolicySpec)
    expect(wrapper.find('[data-test="submit-failure"]').exists()).toBe(true)
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing_policy')
    await submit(wrapper).trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[1][1]).toEqual({ ...first, confirm: { checkbox: true, typed: 'cache_routing_policy' } })
  })

  it('refuses an affinity mode this build does not ship at its field (m4_validate_routing_affinity_unsupported, m4_put_routing_400_unsupported)', async () => {
    const { wrapper, api } = mountPanel('m4_list_default', { validate: m3m4Validation('m4_validate_routing_affinity_unsupported') })
    await flushPromises()
    await picker(wrapper, ROUTING, 'affinity_mode').setValue('prev_host')
    await preview(wrapper, ROUTING)
    expect(api.validatePolicy).toHaveBeenCalledWith(ROUTING, m3m4Draft('m4_validate_routing_affinity_unsupported'))
    expect(field(wrapper, ROUTING, 'affinity_mode').text()).toContain('admin.cacheSettings.errors.unsupported_in_build')
    expect(submit(wrapper).attributes('disabled')).toBeDefined()
    expect(toWriteFailure(m3m4Rejection('m4_put_routing_400_unsupported'))).toEqual({ kind: 'invalid', errors: [{ field: 'affinity_mode', code: 'unsupported_in_build' }] })
    expect(toWriteFailure(m3m4Rejection('m4_put_routing_422_typed'))).toEqual({ kind: 'confirm', missing: ['typed'], typeToConfirm: 'cache_routing_policy' })
  })

  it('turns stickiness off in one step under a neutral badge, no key name (m4_validate_routing_disable_whole_object)', async () => {
    // v1.0.1: the whole nested object, siblings at their stored values, answers exactly as {enabled: false} alone.
    expect(m3m4Validation('m4_validate_routing_disable_whole_object')).toEqual(m3m4Validation('m4_validate_routing_disable'))
    const { wrapper, api } = mountPanel('m4_list_on', { validate: m3m4Validation('m4_validate_routing_disable_whole_object') })
    await flushPromises()
    await field(wrapper, ROUTING, 'keep_sticky_on_busy.enabled').get('[role="switch"]').trigger('click')
    await preview(wrapper, ROUTING)
    expect(api.validatePolicy).toHaveBeenCalledWith(ROUTING, m3m4Draft('m4_validate_routing_disable_whole_object'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-gray')
    expect(wrapper.find('[data-test="diff-keep_sticky_on_busy.enabled"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="diff-keep_sticky_on_busy.group_ids"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    expect(submit(wrapper).attributes('disabled')).toBeUndefined()
  })
})
