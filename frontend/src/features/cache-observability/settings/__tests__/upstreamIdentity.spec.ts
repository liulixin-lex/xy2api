// M3: cache_upstream_identity_policy (L-01) on the generic policy card. The served shape is G's
// cache/p1-l01 (d74eaf603): upstreamIdentitySchema(), policyDefaults(3), risk "high", and
// validationForWrite(), which asks for a reason and a check in any direction but "disable".
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
import type { PoliciesResponse, SchemaField, ValidateResult } from '../types'
import type { PoliciesState } from '../usePolicies'
import { history, rejection, validation as sampleValidation } from './contract'
import { identityEntry, m3Response, rewritten, withEntry } from './fixtures'

/** upstreamIdentitySchema() in cache_policy_upstream_identity.go (d74eaf603), field for field. */
const SERVED_SCHEMA: SchemaField[] = [
  { field: 'mode', type: 'enum', options: ['off', 'uuid_fill'] },
  { field: 'account_ids', type: 'id_list', ref: 'account' },
  { field: 'rollout_percent', type: 'int', min: 0, max: 100, unit: 'percent' },
  { field: 'chat_compat_uuid', type: 'bool' },
  { field: 'header_only_uuid', type: 'bool' },
  { field: 'forward_session_headers', type: 'enum', options: ['whitelist', 'normalize'] },
]

/** As d74eaf603 serves it: no account candidates yet. FE-Q3 now requires them and G adds them; the card must work either way. */
function served(): PoliciesResponse {
  return withEntry(m3Response(), { ...identityEntry(), schema: structuredClone(SERVED_SCHEMA) })
}

/** A validate answer for upstream_identity under G's rules; values only, the shape is the contract's. */
function identityValidation(overrides: Partial<ValidateResult> = {}): ValidateResult {
  return { ...sampleValidation('validate_rev_stale'), current_rev: 4, rev_stale: false, ...overrides }
}

const NO_CONFIRM = { reason_required: false, checkbox_required: false, type_to_confirm: null }

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

function mountPanel(response: PoliciesResponse, validate: ValidateResult = identityValidation()) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(response),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue(history()),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-23T09:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

const field = (wrapper: ReturnType<typeof mountPanel>['wrapper'], name: string) => wrapper.get(`[data-test="field-upstream_identity.${name}"]`)

async function setRollout(wrapper: ReturnType<typeof mountPanel>['wrapper'], value: string) {
  const input = field(wrapper, 'rollout_percent').get('input')
  await input.setValue(value)
  await input.trigger('change')
}

async function preview(wrapper: ReturnType<typeof mountPanel>['wrapper']) {
  await wrapper.get('[data-test="preview-upstream_identity"]').trigger('click')
  await flushPromises()
}

describe('upstream_identity (M3) on the generic policy card', () => {
  beforeEach(() => vi.clearAllMocks())

  it('renders every served field under the master switch, with the high risk tag', async () => {
    const { wrapper } = mountPanel(served())
    await flushPromises()
    const card = wrapper.get('[data-test="master-bar"] [data-test="policy-upstream_identity"]')
    for (const f of SERVED_SCHEMA) expect(card.find(`[data-test="field-upstream_identity.${f.field}"]`).exists(), f.field).toBe(true)
    expect(card.get('[data-test="policy-risk"]').text()).toContain('admin.cacheSettings.risk.high')
    expect(field(wrapper, 'mode').findAll('option').map((o) => o.attributes('value'))).toEqual(['off', 'uuid_fill'])
    expect(field(wrapper, 'forward_session_headers').findAll('option').map((o) => o.attributes('value'))).toEqual(['whitelist', 'normalize'])
    expect(field(wrapper, 'rollout_percent').get('input').attributes()).toMatchObject({ min: '0', max: '100' })
  })

  it('names accounts from the candidates (FE-Q3 final), and by id while a backend sends none', async () => {
    const named = mountPanel(m3Response())
    await flushPromises()
    expect(named.wrapper.get('[data-test="control-account_ids"]').text()).toContain('relay-a-01 (#101)')
    const bare = mountPanel(served())
    await flushPromises()
    const accounts = bare.wrapper.get('[data-test="control-account_ids"]').text()
    expect(accounts).toContain('admin.cacheSettings.ids.item:{"id":101}')
    expect(accounts).toContain('admin.cacheSettings.ids.item:{"id":102}')
  })

  it('adds accounts by id and refuses what is not an id', async () => {
    const { wrapper, state } = mountPanel(served())
    await flushPromises()
    const control = wrapper.get('[data-test="control-account_ids"]')
    await control.get('input').setValue('104, 103')
    await control.findAll('button')[0].trigger('click')
    expect(state.draftOf('upstream_identity').account_ids).toEqual([101, 102, 103, 104])
    await control.get('input').setValue('relay-a')
    await control.findAll('button')[0].trigger('click')
    expect(control.text()).toContain('admin.cacheSettings.ids.invalid')
    expect(state.draftOf('upstream_identity').account_ids).toEqual([101, 102, 103, 104])
  })

  it('locks the two UUID options until the mode is uuid_fill', async () => {
    const { wrapper } = mountPanel(served())
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('off')
    for (const name of ['chat_compat_uuid', 'header_only_uuid']) {
      expect(field(wrapper, name).text(), name).toContain('admin.cacheSettings.locked.requires_mode_uuid_fill')
      expect(field(wrapper, name).get('[role="switch"]').attributes('disabled'), name).toBeDefined()
    }
  })

  it('asks for a reason and a check before widening the rollout, then writes them (high risk, enable)', async () => {
    const enable = identityValidation({ diff: [{ field: 'rollout_percent', before: 50, after: 60 }] })
    const { wrapper, api } = mountPanel(served(), enable)
    await flushPromises()
    await setRollout(wrapper, '60')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith('upstream_identity', { expected_rev: 4, spec: { rollout_percent: 60 } })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    const submit = () => wrapper.get('[data-test="confirm-submit"]')
    expect(submit().attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.reason')
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.checkbox')
    await wrapper.get('[data-test="reason"]').setValue('小比例试运行')
    expect(submit().attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    expect(submit().attributes('disabled')).toBeUndefined()
    api.putPolicy.mockResolvedValueOnce(rewritten(identityEntry(), 5, { rollout_percent: 60 }))
    await submit().trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([
      'upstream_identity',
      { expected_rev: 4, spec: { rollout_percent: 60 }, reason: '小比例试运行', confirm: { checkbox: true, typed: null } },
    ])
  })

  it('turns the fill off in one step under a neutral badge, the reason optional (disable)', async () => {
    const disable = identityValidation({
      diff: [{ field: 'mode', before: 'uuid_fill', after: 'off' }],
      direction: 'disable',
      confirm: NO_CONFIRM,
      effective_preview: { state: 'off', active: false, blockers: [] },
      affected: [],
    })
    const { wrapper, api } = mountPanel(served(), disable)
    await flushPromises()
    await field(wrapper, 'mode').get('select').setValue('off')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith('upstream_identity', { expected_rev: 4, spec: { mode: 'off' } })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-gray')
    expect(wrapper.get('[data-test="dialog"]').text()).toContain('admin.cacheSettings.dialog.optional')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('still asks when a narrower scope comes with a higher share (mixed)', async () => {
    const mixed = identityValidation({
      diff: [
        { field: 'account_ids', before: [101, 102], after: [101] },
        { field: 'rollout_percent', before: 50, after: 60 },
      ],
      direction: 'mixed',
    })
    const { wrapper, api } = mountPanel(served(), mixed)
    await flushPromises()
    const remove = wrapper.get('[data-test="control-account_ids"]').findAll('li button')[1]
    await remove.trigger('click')
    await setRollout(wrapper, '60')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith('upstream_identity', { expected_rev: 4, spec: { account_ids: [101], rollout_percent: 60 } })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.mixed')
    expect(wrapper.get('[data-test="submit-hint"]').text()).toContain('admin.cacheSettings.missing.reason')
  })

  it('shows what this build refuses next to the field: unsupported values and ineligible accounts', async () => {
    const refused = identityValidation({
      valid: false,
      errors: [
        { field: 'forward_session_headers', code: 'unsupported_in_build' },
        { field: 'account_ids', code: 'wrong_platform' },
      ],
      diff: [
        { field: 'account_ids', before: [101, 102], after: [101, 102, 103] },
        { field: 'forward_session_headers', before: 'whitelist', after: 'normalize' },
      ],
      effective_preview: null,
      affected: [],
    })
    const { wrapper } = mountPanel(served(), refused)
    await flushPromises()
    await field(wrapper, 'forward_session_headers').get('select').setValue('normalize')
    const control = wrapper.get('[data-test="control-account_ids"]')
    await control.get('input').setValue('103')
    await control.findAll('button')[0].trigger('click')
    await preview(wrapper)
    expect(field(wrapper, 'forward_session_headers').text()).toContain('admin.cacheSettings.errors.unsupported_in_build')
    expect(field(wrapper, 'account_ids').text()).toContain('admin.cacheSettings.errors.wrong_platform')
  })

  it('reads a 400 CACHE_POLICY_UNSUPPORTED like an invalid write, with its field errors', () => {
    const unsupported = {
      ...rejection('put_400'),
      reason: 'CACHE_POLICY_UNSUPPORTED',
      metadata: { errors: JSON.stringify([{ field: 'header_only_uuid', code: 'unsupported_in_build' }]) },
    }
    expect(toWriteFailure(unsupported)).toMatchObject({ kind: 'invalid', errors: [{ field: 'header_only_uuid', code: 'unsupported_in_build' }] })
  })
})
