// T2.3 (L-03): metadata_account_ids added to cache_anthropic_identity_policy after metadata_group_ids,
// driven by F's m4l03 samples (00-shared/cache_policies_contract/m4l03 v1.0.1, README §1 and §4). The
// relays are picked from the served candidates or typed by id; while the candidates were not loaded only
// typing is left, and the server checks what is added. Unlike upstream identity, an empty list has no effect.
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ success: vi.fn(), leave: vi.fn(), copy: undefined as unknown }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    // Answers from the zh tree, so a field without its own copy falls back as it does in the app.
    te: (key: string) => typeof key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], mocks.copy) === 'string',
    locale: { value: 'zh' },
  }),
}))
vi.mock('vue-router', () => ({ onBeforeRouteLeave: mocks.leave }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: {} } }))
vi.mock('@/api/client', () => ({ apiClient: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }))

import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import CacheSettingsPanel from '../CacheSettingsPanel.vue'
import { toWriteFailure } from '../api'
import { controlIdFor } from '../catalog'
import type { PoliciesResponse, PolicyEntry, ValidateResult, WriteBody } from '../types'
import type { PoliciesState } from '../usePolicies'
import type { ContractSample } from './contract'

mocks.copy = zh

const files = import.meta.glob('./contract/m4l03/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/m4l03/', '').replace('.json', ''))
function m4l03(name: string): ContractSample {
  const found = files[`./contract/m4l03/${name}.json`]
  if (!found) throw new Error(`unknown m4l03 sample ${name}`)
  return structuredClone(found)
}
const data = <T>(name: string) => m4l03(name).response.data as T
const body = (name: string) => m4l03(name).request.body as WriteBody
/** What `apiClient` rejects with for an error sample (as `rejection` in ./contract). */
function rejection(name: string) {
  const s = m4l03(name)
  return { status: s.status, code: s.response.code, reason: s.response.reason, message: s.response.message, metadata: s.response.metadata }
}

const KEY = 'anthropic_identity'
const FIELD = 'metadata_account_ids'
const GROUPS = [
  { id: 31, name: 'claude-pool-a', platform: 'anthropic' },
  { id: 32, name: 'claude-pool-b', platform: 'anthropic' },
  { id: 77, name: 'claude-standby', platform: 'anthropic' },
  { id: 90, name: 'openai-pool', platform: 'openai' },
]

const stubs = {
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

function mountPanel(list: string | PoliciesResponse, validate?: ValidateResult) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(typeof list === 'string' ? data<PoliciesResponse>(list) : list),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue({ items: [], history_complete: true, history_incomplete_reason: null }),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue(structuredClone(GROUPS)), now: () => new Date('2026-10-25T06:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']
const field = (wrapper: Wrapper, name: string, key = KEY) => wrapper.get(`[data-test="field-${key}.${name}"]`)
const offered = (wrapper: Wrapper, name: string, key = KEY) => field(wrapper, name, key).get('select').findAll('option').map((o) => o.attributes('value'))
const anthropic = (list: PoliciesResponse) => list.policies.find((p) => p.key === KEY) as PolicyEntry

async function pick(wrapper: Wrapper, name: string, id: number, key = KEY) {
  await field(wrapper, name, key).get('select').setValue(String(id))
}

async function typeIds(wrapper: Wrapper, text: string) {
  await field(wrapper, FIELD).get('input').setValue(text)
  await field(wrapper, FIELD).get('button.btn-secondary').trigger('click')
}

async function setNumber(wrapper: Wrapper, name: string, value: string) {
  const input = field(wrapper, name).get('input')
  await input.setValue(value)
  await input.trigger('change')
}

async function preview(wrapper: Wrapper) {
  await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
  await flushPromises()
}

async function confirmAndSubmit(wrapper: Wrapper, reason: string) {
  await wrapper.get('[data-test="reason"]').setValue(reason)
  await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
  await wrapper.get('[data-test="confirm-submit"]').trigger('click')
  await flushPromises()
}

function at(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

describe('anthropic_identity relays in the T2.3 build (m4l03 samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the field, its empty list, the picker and the two errors', () => {
    expect(NAMES.sort()).toEqual([
      'm4l03_list_account_options_unavailable',
      'm4l03_list_t23',
      'm4l03_m4l04_list_t22',
      'm4l03_put_400_official_endpoint',
      'm4l03_put_metadata_enable_200',
      'm4l03_validate_account_rejected',
      'm4l03_validate_accounts_clear',
      'm4l03_validate_metadata_enable',
    ])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const part of ['label', 'description', 'risk', 'empty']) {
        expect(typeof at(root, `admin.cacheSettings.fields.${KEY}.${FIELD}.${part}`), `${lang} ${part}`).toBe('string')
      }
      expect(at(root, `admin.cacheSettings.fields.${KEY}.${FIELD}.empty`), lang).toMatch(lang === 'zh' ? /为空时不生效/ : /No effect when empty/)
      // Upstream identity keeps the generic hint: there an empty list means every eligible account.
      expect(at(root, 'admin.cacheSettings.fields.upstream_identity.account_ids.empty'), lang).toBeUndefined()
      for (const path of ['errors.official_endpoint', 'errors.not_apikey', 'accountPicker.placeholder', 'accountPicker.empty', 'ids.manual']) {
        expect(typeof at(root, `admin.cacheSettings.${path}`), `${lang} ${path}`).toBe('string')
      }
    }
  })

  it('offers the served relays right after the metadata groups and names the stored one (m4l03_list_t23)', async () => {
    const { wrapper, state } = mountPanel('m4l03_list_t23')
    await flushPromises()
    const rows = wrapper.get(`[data-test="policy-${KEY}"]`).findAll(`[data-test^="field-${KEY}."]`).map((row) => row.attributes('data-test'))
    expect(rows).toEqual(anthropic(data<PoliciesResponse>('m4l03_list_t23')).schema.map((f) => `field-${KEY}.${f.field}`))
    expect(rows.indexOf(`field-${KEY}.${FIELD}`)).toBe(rows.indexOf(`field-${KEY}.metadata_group_ids`) + 1)
    expect(field(wrapper, FIELD).text()).toContain('relay-claude-a (#501)')
    expect(field(wrapper, FIELD).text()).not.toContain('admin.cacheSettings.locked.')
    expect(offered(wrapper, FIELD)).toEqual(['502'])
    await pick(wrapper, FIELD, 502)
    expect(state.draftOf(KEY)[FIELD]).toEqual([501, 502])
    expect(offered(wrapper, FIELD)).toEqual([])
  })

  it('asks for a reason and a check when the relays are first chosen, and writes the sample body (m4l03_validate_metadata_enable, m4l03_put_metadata_enable_200)', async () => {
    // Rev 2 as the T2.3 build serves it: the stored value of m4l03_m4l04_list_t22, without the T2.2 build's marks.
    const list = data<PoliciesResponse>('m4l03_m4l04_list_t22')
    for (const f of anthropic(list).schema) delete f.not_implemented_in_build
    const { wrapper, api } = mountPanel(list, data<ValidateResult>('m4l03_validate_metadata_enable'))
    await flushPromises()
    await pick(wrapper, 'metadata_group_ids', 31)
    await pick(wrapper, FIELD, 501)
    await setNumber(wrapper, 'metadata_rollout_percent', '20')
    await preview(wrapper)
    const sample = body('m4l03_validate_metadata_enable')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.enable')
    api.putPolicy.mockResolvedValueOnce(data<PolicyEntry>('m4l03_put_metadata_enable_200'))
    await confirmAndSubmit(wrapper, sample.reason as string)
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, body('m4l03_put_metadata_enable_200')])
    expect(field(wrapper, FIELD).text()).toContain('relay-claude-a (#501)')
    expect(field(wrapper, FIELD).get('select').attributes('disabled')).toBeUndefined()
  })

  it('shows an official endpoint and a non API key account at the field (m4l03_validate_account_rejected)', async () => {
    const { wrapper, api, state } = mountPanel('m4l03_list_t23', data<ValidateResult>('m4l03_validate_account_rejected'))
    await flushPromises()
    // Neither is a candidate, so they come in by id.
    await typeIds(wrapper, '503, 504')
    expect(state.draftOf(KEY)[FIELD]).toEqual([501, 503, 504])
    await preview(wrapper)
    const sample = body('m4l03_validate_account_rejected')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    const text = field(wrapper, FIELD).text()
    expect(text).toContain('admin.cacheSettings.errors.official_endpoint')
    expect(text).toContain('admin.cacheSettings.errors.not_apikey')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
  })

  it('brings the refusal of a PUT to the field (m4l03_put_400_official_endpoint)', async () => {
    expect(toWriteFailure(rejection('m4l03_put_400_official_endpoint'))).toEqual({ kind: 'invalid', errors: [{ field: FIELD, code: 'official_endpoint' }] })
    // A validate that passed, then the account moved to the official endpoint before the PUT.
    const passed = { ...data<ValidateResult>('m4l03_validate_account_rejected'), valid: true, errors: [], diff: [{ field: FIELD, before: [501], after: [501, 503] }] }
    const { wrapper, api } = mountPanel('m4l03_list_t23', passed)
    await flushPromises()
    await typeIds(wrapper, '503')
    await preview(wrapper)
    api.putPolicy.mockRejectedValueOnce(rejection('m4l03_put_400_official_endpoint'))
    const sample = body('m4l03_put_400_official_endpoint')
    await confirmAndSubmit(wrapper, sample.reason as string)
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, sample])
    expect(field(wrapper, FIELD).text()).toContain('admin.cacheSettings.errors.official_endpoint')
    expect(wrapper.find('[data-test="submit-failure"]').exists()).toBe(true)
  })

  it('clears the relays as a disable without asking, and says that an empty list has no effect (m4l03_validate_accounts_clear)', async () => {
    const { wrapper, api, state } = mountPanel('m4l03_list_t23', data<ValidateResult>('m4l03_validate_accounts_clear'))
    await flushPromises()
    await field(wrapper, FIELD).get('li button').trigger('click')
    expect(state.draftOf(KEY)[FIELD]).toEqual([])
    expect(field(wrapper, FIELD).text()).toContain(`admin.cacheSettings.fields.${KEY}.${FIELD}.empty`)
    expect(field(wrapper, FIELD).text()).not.toContain('admin.cacheSettings.ids.emptyMeansAll')
    // The empty account list of upstream identity still reads as every eligible account.
    expect(field(wrapper, 'account_ids', 'upstream_identity').text()).toContain('admin.cacheSettings.ids.emptyMeansAll')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, body('m4l03_validate_accounts_clear'))
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.disable')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('falls back to typing ids while the relays were not loaded, and keeps the group candidates (m4l03_list_account_options_unavailable)', async () => {
    const { wrapper, state } = mountPanel('m4l03_list_account_options_unavailable')
    await flushPromises()
    expect(wrapper.get(`[data-test="policy-${KEY}"] [data-test="policy-warnings"]`).text()).toContain('admin.cacheSettings.warnings.account_options_unavailable')
    const relays = field(wrapper, FIELD)
    expect(relays.find('select').exists()).toBe(false)
    expect(relays.text()).toContain('admin.cacheSettings.ids.item:{"id":501}')
    expect(relays.get('input').attributes('disabled')).toBeUndefined()
    await typeIds(wrapper, '502')
    expect(state.draftOf(KEY)[FIELD]).toEqual([501, 502])
    // The warning is about accounts only: the groups keep their served candidates.
    expect(offered(wrapper, 'metadata_group_ids')).toEqual(['32', '77'])
    expect(offered(wrapper, 'anchor_group_ids')).toEqual(['77'])
  })

  it('reads an empty group list beside the account warning as no eligible group, not as unloaded', async () => {
    const list = data<PoliciesResponse>('m4l03_list_account_options_unavailable')
    const groups = anthropic(list).schema.find((f) => f.field === 'metadata_group_ids')!
    groups.options = []
    const { wrapper } = mountPanel(list)
    await flushPromises()
    // Not the admin group list: that fallback belongs to group_options_unavailable.
    expect(offered(wrapper, 'metadata_group_ids')).toEqual([])
    expect(field(wrapper, 'metadata_group_ids').text()).toContain('claude-pool-a (#31)')
  })

  it('locks the relays in a build without metadata generation (m4l03_m4l04_list_t22)', async () => {
    const { wrapper } = mountPanel('m4l03_m4l04_list_t22')
    await flushPromises()
    const relays = field(wrapper, FIELD)
    expect(relays.text()).toContain('admin.cacheSettings.locked.not_implemented_in_build')
    expect(relays.get('select').attributes('disabled')).toBeDefined()
    expect(relays.get('input').attributes('disabled')).toBeDefined()
    expect(relays.text()).toContain(`admin.cacheSettings.fields.${KEY}.${FIELD}.empty`)
  })

  it('offers the upstream identity accounts from the same picker, typing beside it (m4l03_list_t23)', async () => {
    const { wrapper, state } = mountPanel('m4l03_list_t23')
    await flushPromises()
    expect(offered(wrapper, 'account_ids', 'upstream_identity')).toEqual(['101', '102', '103'])
    const accounts = field(wrapper, 'account_ids', 'upstream_identity')
    expect(accounts.get('select').attributes('id')).toBe(controlIdFor('upstream_identity', 'account_ids'))
    expect(accounts.get('input').attributes('aria-label')).toBe('admin.cacheSettings.ids.manual')
    await pick(wrapper, 'account_ids', 102, 'upstream_identity')
    expect(state.draftOf('upstream_identity').account_ids).toEqual([102])
  })
})
