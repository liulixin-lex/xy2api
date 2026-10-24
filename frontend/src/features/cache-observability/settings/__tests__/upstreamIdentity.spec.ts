// M3: cache_upstream_identity_policy (L-01) on the generic policy card, driven by F's m3m4
// samples (v1.0.3, M3 part; checked against G's cache/p1-l01 d74eaf603): schema, defaults,
// risk "high", and a reason plus a check in any direction but "disable".
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
import type { PoliciesResponse, ValidateResult } from '../types'
import type { PoliciesState } from '../usePolicies'
import { M3M4_NAMES, m3m4Body, m3m4Draft, m3m4History, m3m4List, m3m4Rejection, m3m4Validation, m3m4Written } from './m3m4'

const KEY = 'upstream_identity'

function identity(response: PoliciesResponse) {
  const found = response.policies.find((p) => p.key === KEY)
  if (!found) throw new Error(`no ${KEY}`)
  return found
}

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

function mountPanel(list: string | PoliciesResponse, validate?: ValidateResult) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(typeof list === 'string' ? m3m4List(list) : list),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue(m3m4History('m3_history')),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-24T09:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']

const field = (wrapper: Wrapper, name: string) => wrapper.get(`[data-test="field-${KEY}.${name}"]`)

async function setRollout(wrapper: Wrapper, value: string) {
  const input = field(wrapper, 'rollout_percent').get('input')
  await input.setValue(value)
  await input.trigger('change')
}

async function addAccounts(wrapper: Wrapper, text: string) {
  const control = wrapper.get('[data-test="control-account_ids"]')
  await control.get('input').setValue(text)
  await control.findAll('button')[0].trigger('click')
}

async function preview(wrapper: Wrapper) {
  await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
  await flushPromises()
}

/** Fills what the dialog asks for and submits; the reason is the sample's. */
async function confirmAndSubmit(wrapper: Wrapper, reason: string) {
  await wrapper.get('[data-test="reason"]').setValue(reason)
  await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
  await wrapper.get('[data-test="confirm-submit"]').trigger('click')
  await flushPromises()
}

describe('upstream_identity (M3) on the generic policy card, m3m4 samples', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the M3 samples the card uses', () => {
    expect(M3M4_NAMES.filter((n) => n.startsWith('m3_'))).toHaveLength(22)
  })

  it('renders every field of the served schema under the master switch, with the high risk tag', async () => {
    const { wrapper } = mountPanel('m3_list_masked_by_master')
    await flushPromises()
    const card = wrapper.get(`[data-test="master-bar"] [data-test="policy-${KEY}"]`)
    for (const f of identity(m3m4List('m3_list_masked_by_master')).schema) {
      expect(card.find(`[data-test="field-${KEY}.${f.field}"]`).exists(), f.field).toBe(true)
    }
    expect(card.get('[data-test="policy-risk"]').text()).toContain('admin.cacheSettings.risk.high')
    expect(field(wrapper, 'mode').findAll('option').map((o) => o.attributes('value'))).toEqual(['off', 'uuid_fill'])
    expect(field(wrapper, 'forward_session_headers').findAll('option').map((o) => o.attributes('value'))).toEqual(['whitelist', 'normalize'])
    expect(field(wrapper, 'rollout_percent').get('input').attributes()).toMatchObject({ min: '0', max: '100' })

    const off = mountPanel('m3_list_observability_off')
    await flushPromises()
    expect(off.wrapper.get(`[data-test="policy-${KEY}"] [data-test="policy-warnings"]`).text()).toContain('admin.cacheSettings.warnings.observability_off')
  })

  it('names accounts from the candidates (FE-Q3), and by id while a backend sends none', async () => {
    const named = mountPanel('m3_list_on')
    await flushPromises()
    expect(named.wrapper.get('[data-test="control-account_ids"]').text()).toContain('relay-a-01 (#101)')
    // G's d74eaf603 still serves account_ids without options; the card must work either way.
    const bare = m3m4List('m3_list_on')
    for (const f of identity(bare).schema) if (f.field === 'account_ids') delete f.options
    const unnamed = mountPanel(bare)
    await flushPromises()
    const accounts = unnamed.wrapper.get('[data-test="control-account_ids"]').text()
    expect(accounts).toContain('admin.cacheSettings.ids.item:{"id":101}')
    expect(accounts).toContain('admin.cacheSettings.ids.item:{"id":102}')
  })

  it('adds accounts by id, never twice (m3_validate_account_ids_duplicate), and refuses what is not an id', async () => {
    const { wrapper, state } = mountPanel('m3_list_on')
    await flushPromises()
    await addAccounts(wrapper, '103, 101, 103')
    expect(state.draftOf(KEY).account_ids).toEqual([101, 102, 103])
    await addAccounts(wrapper, 'relay-a')
    expect(wrapper.get('[data-test="control-account_ids"]').text()).toContain('admin.cacheSettings.ids.invalid')
    expect(state.draftOf(KEY).account_ids).toEqual([101, 102, 103])
  })

  it('locks the two UUID options until the mode is uuid_fill, the rule of m3_validate_chat_compat_requires_uuid_fill', async () => {
    expect(m3m4Validation('m3_validate_chat_compat_requires_uuid_fill').errors).toEqual([{ field: 'chat_compat_uuid', code: 'requires_mode_uuid_fill' }])
    const { wrapper } = mountPanel('m3_list_default')
    await flushPromises()
    for (const name of ['chat_compat_uuid', 'header_only_uuid']) {
      expect(field(wrapper, name).text(), name).toContain('admin.cacheSettings.locked.requires_mode_uuid_fill')
      expect(field(wrapper, name).get('[role="switch"]').attributes('disabled'), name).toBeDefined()
    }
    await field(wrapper, 'mode').get('select').setValue('uuid_fill')
    expect(field(wrapper, 'chat_compat_uuid').get('[role="switch"]').attributes('disabled')).toBeUndefined()
  })

  it('asks for a reason and a check before turning the fill on, names the warning, and writes the sample body (m3_validate_enable)', async () => {
    const { wrapper, api } = mountPanel('m3_list_default', m3m4Validation('m3_validate_enable'))
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('uuid_fill')
    await setRollout(wrapper, '50')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, m3m4Draft('m3_validate_enable'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.enable')
    expect(wrapper.get('[data-test="validate-warnings"]').text()).toContain('admin.cacheSettings.warnings.observability_off')
    const submit = () => wrapper.get('[data-test="confirm-submit"]')
    expect(submit().attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.reason')
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.checkbox')
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    api.putPolicy.mockResolvedValueOnce(m3m4Written('m3_put_200'))
    await confirmAndSubmit(wrapper, m3m4Body('m3_validate_enable').reason as string)
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, m3m4Body('m3_validate_enable')])
  })

  it('says a zero share has no effect (m3_validate_rollout_zero_warning)', async () => {
    const { wrapper, api } = mountPanel('m3_list_default', m3m4Validation('m3_validate_rollout_zero_warning'))
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('uuid_fill')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, m3m4Draft('m3_validate_rollout_zero_warning'))
    expect(wrapper.get('[data-test="validate-warnings"]').text()).toContain('admin.cacheSettings.warnings.no_effect_rollout_zero')
  })

  it('turns the fill off in one step under a neutral badge, the reason optional (m3_validate_disable)', async () => {
    const { wrapper, api } = mountPanel('m3_list_on', m3m4Validation('m3_validate_disable'))
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('off')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, m3m4Draft('m3_validate_disable'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-gray')
    expect(wrapper.get('[data-test="dialog"]').text()).toContain('admin.cacheSettings.dialog.optional')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('still asks when a narrower scope comes with a higher share (m3_validate_mixed)', async () => {
    const { wrapper, api } = mountPanel('m3_list_on', m3m4Validation('m3_validate_mixed'))
    await flushPromises()
    await wrapper.get('[data-test="control-account_ids"]').findAll('li button')[1].trigger('click')
    await setRollout(wrapper, '80')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, m3m4Draft('m3_validate_mixed'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.mixed')
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.reason')
  })

  it('shows unknown and ineligible accounts at the field (m3_validate_account_ids_invalid)', async () => {
    const { wrapper, api } = mountPanel('m3_list_default', m3m4Validation('m3_validate_account_ids_invalid'))
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('uuid_fill')
    await addAccounts(wrapper, '101, 999, 201')
    await preview(wrapper)
    // The card keeps ids sorted; the sample lists them as typed.
    const draft = m3m4Draft('m3_validate_account_ids_invalid')
    const sent = api.validatePolicy.mock.calls[0][1]
    expect(sent.expected_rev).toBe(draft.expected_rev)
    expect(sent.spec).toEqual({ ...draft.spec, account_ids: [...(draft.spec.account_ids as number[])].sort((a, b) => a - b) })
    const accounts = field(wrapper, 'account_ids').text()
    expect(accounts).toContain('admin.cacheSettings.errors.unknown_account')
    expect(accounts).toContain('admin.cacheSettings.errors.wrong_platform')
    expect(accounts).toContain('admin.cacheSettings.ids.item:{"id":999}')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
  })

  it('shows values this build refuses at their fields, from validate (m3_validate_unsupported_in_build) and from the PUT (m3_put_400_unsupported)', async () => {
    const refused = mountPanel('m3_list_on', m3m4Validation('m3_validate_unsupported_in_build'))
    await flushPromises()
    await field(refused.wrapper, 'header_only_uuid').get('[role="switch"]').trigger('click')
    await field(refused.wrapper, 'forward_session_headers').get('select').setValue('normalize')
    await preview(refused.wrapper)
    expect(refused.api.validatePolicy).toHaveBeenCalledWith(KEY, m3m4Draft('m3_put_400_unsupported'))
    for (const name of ['header_only_uuid', 'forward_session_headers']) {
      expect(field(refused.wrapper, name).text(), name).toContain('admin.cacheSettings.errors.unsupported_in_build')
    }
    expect(refused.wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()

    // A validate that passed, then a PUT the server refuses: the same errors reach the fields.
    const passed = { ...m3m4Validation('m3_validate_enable'), current_rev: 4, warnings: [] }
    const late = mountPanel('m3_list_on', passed)
    await flushPromises()
    await field(late.wrapper, 'header_only_uuid').get('[role="switch"]').trigger('click')
    await field(late.wrapper, 'forward_session_headers').get('select').setValue('normalize')
    await preview(late.wrapper)
    late.api.putPolicy.mockRejectedValueOnce(m3m4Rejection('m3_put_400_unsupported'))
    await confirmAndSubmit(late.wrapper, m3m4Body('m3_put_400_unsupported').reason as string)
    expect(late.api.putPolicy.mock.calls[0]).toEqual([KEY, m3m4Body('m3_put_400_unsupported')])
    for (const name of ['header_only_uuid', 'forward_session_headers']) {
      expect(field(late.wrapper, name).text(), name).toContain('admin.cacheSettings.errors.unsupported_in_build')
    }
    expect(late.wrapper.find('[data-test="submit-failure"]').exists()).toBe(true)
  })

  it('lists an error on a field outside the schema (m3_validate_forbidden_field)', async () => {
    const { wrapper } = mountPanel('m3_list_on', m3m4Validation('m3_validate_forbidden_field'))
    await flushPromises()
    await setRollout(wrapper, '60')
    await preview(wrapper)
    expect(wrapper.get('[data-test="validate-errors"]').text()).toContain('admin.cacheSettings.errors.forbidden_field')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
  })

  it('keeps accounts editable when their candidates were not loaded, and reads them again (m3_list_account_options_unavailable)', async () => {
    const { wrapper, api, state } = mountPanel('m3_list_account_options_unavailable')
    await flushPromises()
    const card = () => wrapper.get(`[data-test="policy-${KEY}"]`)
    expect(card().get('[data-test="policy-warnings"]').text()).toContain('admin.cacheSettings.warnings.account_options_unavailable')
    // "Not loaded" is not "none eligible": the stored accounts stay, by id, and more can be added.
    const accounts = wrapper.get('[data-test="control-account_ids"]')
    expect(accounts.text()).toContain('admin.cacheSettings.ids.item:{"id":101}')
    expect(accounts.get('input').attributes('disabled')).toBeUndefined()
    await addAccounts(wrapper, '103')
    expect(state.draftOf(KEY).account_ids).toEqual([101, 102, 103])
    const reads = api.listPolicies.mock.calls.length
    api.listPolicies.mockResolvedValue(m3m4List('m3_list_on'))
    await card().get('[data-test="reload-candidates"]').trigger('click')
    await flushPromises()
    expect(api.listPolicies).toHaveBeenCalledTimes(reads + 1)
    expect(card().find('[data-test="policy-warnings"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="control-account_ids"]').text()).toContain('relay-a-01 (#101)')
    expect(state.draftOf(KEY).account_ids).toEqual([101, 102, 103])

    // GET of the key: the hint goes last, after the warnings already there (m3_get_account_options_unavailable).
    const list = m3m4List('m3_list_on')
    list.policies = list.policies.map((p) => (p.key === KEY ? m3m4Written('m3_get_account_options_unavailable') : p))
    const both = mountPanel(list)
    await flushPromises()
    const warnings = both.wrapper.get(`[data-test="policy-${KEY}"] [data-test="policy-warnings"]`)
    expect(warnings.findAll('li').map((li) => li.text())).toEqual([
      'admin.cacheSettings.warnings.observability_off',
      expect.stringContaining('admin.cacheSettings.warnings.account_options_unavailable'),
    ])
    expect(warnings.findAll('[data-test="reload-candidates"]')).toHaveLength(1)
  })

  it('reads the candidates again after a disable, whose PUT leaves them out (m3_put_disable_skip_account_options)', async () => {
    const { wrapper, api } = mountPanel('m3_list_on', m3m4Validation('m3_validate_disable'))
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('off')
    await preview(wrapper)
    const saved = m3m4Written('m3_put_disable_skip_account_options')
    const reread = m3m4List('m3_list_on')
    reread.policies = reread.policies.map((p) => (p.key === KEY ? { ...saved, schema: p.schema, effective: { ...saved.effective, warnings: [] } } : p))
    api.putPolicy.mockResolvedValueOnce(saved)
    api.listPolicies.mockResolvedValue(reread)
    const reads = api.listPolicies.mock.calls.length
    const body = m3m4Body('m3_put_disable_skip_account_options')
    await wrapper.get('[data-test="reason"]').setValue(body.reason as string)
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, { ...body, confirm: { checkbox: false, typed: null } }])
    expect(api.listPolicies).toHaveBeenCalledTimes(reads + 1)
    expect(wrapper.find(`[data-test="policy-${KEY}"] [data-test="policy-warnings"]`).exists()).toBe(false)
    expect(wrapper.get('[data-test="control-account_ids"]').text()).toContain('relay-a-01 (#101)')
  })

  it('narrows to ids the server does not look up as a plain disable (m3_validate_disable_ids_unverified)', async () => {
    const { wrapper, api } = mountPanel('m3_list_default', m3m4Validation('m3_validate_disable_ids_unverified'))
    await flushPromises()
    await addAccounts(wrapper, '101, 999')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, m3m4Draft('m3_validate_disable_ids_unverified'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-gray')
    expect(field(wrapper, 'account_ids').text()).not.toContain('admin.cacheSettings.errors.')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('reads the M3 error envelopes: 400 unsupported and invalid, 422 confirm', () => {
    expect(toWriteFailure(m3m4Rejection('m3_put_400_unsupported'))).toEqual({
      kind: 'invalid',
      errors: [
        { field: 'forward_session_headers', code: 'unsupported_in_build' },
        { field: 'header_only_uuid', code: 'unsupported_in_build' },
      ],
    })
    expect(toWriteFailure(m3m4Rejection('m3_put_400_invalid'))).toEqual({ kind: 'invalid', errors: [{ field: 'chat_compat_uuid', code: 'requires_mode_uuid_fill' }] })
    expect(toWriteFailure(m3m4Rejection('m3_put_422_confirm'))).toEqual({ kind: 'confirm', missing: ['reason', 'checkbox'], typeToConfirm: null })
  })
})
