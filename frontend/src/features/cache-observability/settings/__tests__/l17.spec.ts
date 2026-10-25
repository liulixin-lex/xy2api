// T3.4 (L-17): three Grok fields added to cache_upstream_identity_policy after max_inflight_per_key,
// driven by F's l17 samples (00-shared/cache_policies_contract/l17 v1.1, README §1, §2 and §4).
// grok.chat_body_key and grok.compact_identity are switches, default off, independent of the session
// identifier fill; grok.account_ids lists the Grok accounts they apply to, and an empty list has no
// effect. All three are capability fields of l17: a build without it marks them
// not_implemented_in_build, and the X-B1 lock keeps them at their default.
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

import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'
import CacheSettingsPanel from '../CacheSettingsPanel.vue'
import { toWriteFailure } from '../api'
import type { PoliciesResponse, PolicyEntry, PolicySpec, ValidateResult, WriteBody } from '../types'
import type { PoliciesState } from '../usePolicies'
import type { ContractSample } from './contract'

const files = import.meta.glob('./contract/l17/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/l17/', '').replace('.json', ''))
function l17(name: string): ContractSample {
  const found = files[`./contract/l17/${name}.json`]
  if (!found) throw new Error(`unknown l17 sample ${name}`)
  return structuredClone(found)
}
const data = <T>(name: string) => l17(name).response.data as T
const body = (name: string) => l17(name).request.body as WriteBody
/** What `apiClient` rejects with for an error sample (as `rejection` in ./contract). */
function rejection(name: string) {
  const s = l17(name)
  return { status: s.status, code: s.response.code, reason: s.response.reason, message: s.response.message, metadata: s.response.metadata }
}

const KEY = 'upstream_identity'
const FIELDS = ['grok.chat_body_key', 'grok.compact_identity']
const ACCOUNTS = 'grok.account_ids'

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
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-25T12:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']
const card = (wrapper: Wrapper) => wrapper.get(`[data-test="policy-${KEY}"]`)
const field = (wrapper: Wrapper, name: string) => wrapper.get(`[data-test="field-${KEY}.${name}"]`)
const toggle = (wrapper: Wrapper, name: string) => field(wrapper, name).get('[role="switch"]')
const offered = (wrapper: Wrapper, name: string) => field(wrapper, name).get('select').findAll('option').map((o) => o.attributes('value'))
const entry = (list: PoliciesResponse) => list.policies.find((p) => p.key === KEY) as PolicyEntry
/** The same stored value as a build with l17 serves it: the marks of the build without it removed. */
function withL17(name: string): PoliciesResponse {
  const list = data<PoliciesResponse>(name)
  for (const f of entry(list).schema) delete f.not_implemented_in_build
  return list
}

async function typeIds(wrapper: Wrapper, name: string, text: string) {
  await field(wrapper, name).get('input').setValue(text)
  await field(wrapper, name).get('button.btn-secondary').trigger('click')
}

async function preview(wrapper: Wrapper) {
  await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
  await flushPromises()
}

function at(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

describe('upstream_identity Grok switches (l17 samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the three fields', () => {
    expect(NAMES.sort()).toEqual([
      'l17_dormant_list_db_enabled',
      'l17_dormant_put_400_unsupported',
      'l17_dormant_validate_unsupported',
      'l17_list_grok_only_master_off',
      'l17_list_on',
      'l17_m31_m3_list_account_options_unavailable',
      'l17_m31_m3_list_on',
      'l17_put_200',
      'l17_validate_account_rejected',
      'l17_validate_accounts_clear',
      'l17_validate_disable',
      'l17_validate_enable',
    ])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const name of ['grok_chat_body_key', 'grok_compact_identity', 'grok_account_ids']) {
        for (const part of ['label', 'description', 'risk']) {
          expect(typeof at(root, `admin.cacheSettings.fields.${KEY}.${name}.${part}`), `${lang} ${name} ${part}`).toBe('string')
        }
      }
      // Unlike the accounts of the identifier fill, an empty Grok list has no effect.
      expect(at(root, `admin.cacheSettings.fields.${KEY}.grok_account_ids.empty`), lang).toMatch(lang === 'zh' ? /为空时不生效/ : /No effect when empty/)
      expect(at(root, `admin.cacheSettings.fields.${KEY}.account_ids.empty`), lang).toBeUndefined()
    }
    expect(at(zh, `admin.cacheSettings.fields.${KEY}.grok_compact_identity.description`)).toContain('不会额外添加工具')
  })

  it('renders the three fields after the in-flight cap, as stored, with the Grok accounts as candidates (l17_list_on)', async () => {
    const { wrapper } = mountPanel('l17_list_on')
    await flushPromises()
    const rows = card(wrapper).findAll(`[data-test^="field-${KEY}."]`).map((row) => row.attributes('data-test'))
    expect(rows.slice(-4)).toEqual([`field-${KEY}.max_inflight_per_key`, ...[...FIELDS, ACCOUNTS].map((f) => `field-${KEY}.${f}`)])
    expect(field(wrapper, ACCOUNTS).text()).toContain('grok-oauth-a (#701)')
    expect(offered(wrapper, ACCOUNTS)).toEqual(['702'])
    for (const name of FIELDS) {
      expect(toggle(wrapper, name).attributes('aria-checked'), name).toBe('true')
      expect(toggle(wrapper, name).attributes('disabled'), name).toBeUndefined()
    }
    expect(card(wrapper).attributes('data-state')).toBe('on')
  })

  it('turns both on for one account as an enable that asks for a reason and a check, and writes the sample body (l17_validate_enable, l17_put_200)', async () => {
    const { wrapper, api } = mountPanel(withL17('l17_m31_m3_list_on'), data<ValidateResult>('l17_validate_enable'))
    await flushPromises()
    for (const name of FIELDS) await toggle(wrapper, name).trigger('click')
    expect(field(wrapper, ACCOUNTS).text()).toContain(`admin.cacheSettings.fields.${KEY}.grok_account_ids.empty`)
    await field(wrapper, ACCOUNTS).get('select').setValue('701')
    await preview(wrapper)
    const sample = body('l17_validate_enable')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.enable')
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    api.putPolicy.mockResolvedValueOnce(data<PolicyEntry>('l17_put_200'))
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, sample])
  })

  it('turns one off as a disable without asking (l17_validate_disable)', async () => {
    const answer = data<ValidateResult>('l17_validate_disable')
    const { wrapper, api } = mountPanel('l17_list_on', { ...answer, current_rev: 4 })
    await flushPromises()
    await toggle(wrapper, 'grok.chat_body_key').trigger('click')
    await preview(wrapper)
    // The card sends the nested object whole; the sample sends only the changed key. Both mean the same.
    const sent = api.validatePolicy.mock.calls[0][1] as WriteBody
    expect(sent.expected_rev).toBe(4)
    expect(sent.spec.grok).toMatchObject(body('l17_validate_disable').spec.grok as PolicySpec)
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.disable')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('shows unknown and non Grok accounts at the field (l17_validate_account_rejected)', async () => {
    const answer = data<ValidateResult>('l17_validate_account_rejected')
    const { wrapper, api } = mountPanel('l17_list_on', { ...answer, current_rev: 4 })
    await flushPromises()
    // Neither is a candidate, so they come in by id.
    await typeIds(wrapper, ACCOUNTS, '501, 999')
    await preview(wrapper)
    const sent = api.validatePolicy.mock.calls[0][1] as WriteBody
    expect(sent.expected_rev).toBe(4)
    expect((sent.spec.grok as PolicySpec).account_ids).toEqual([...((body('l17_validate_account_rejected').spec.grok as PolicySpec).account_ids as number[])].sort((a, b) => a - b))
    const text = field(wrapper, ACCOUNTS).text()
    expect(text).toContain('admin.cacheSettings.errors.wrong_platform')
    expect(text).toContain('admin.cacheSettings.errors.unknown_account')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
  })

  it('clears the Grok accounts as a disable without asking, and says an empty list has no effect (l17_validate_accounts_clear)', async () => {
    const answer = data<ValidateResult>('l17_validate_accounts_clear')
    const { wrapper, api } = mountPanel('l17_list_on', { ...answer, current_rev: 4 })
    await flushPromises()
    await field(wrapper, ACCOUNTS).get('li button').trigger('click')
    expect(field(wrapper, ACCOUNTS).text()).toContain(`admin.cacheSettings.fields.${KEY}.grok_account_ids.empty`)
    await preview(wrapper)
    const sent = api.validatePolicy.mock.calls[0][1] as WriteBody
    expect(sent.spec.grok).toMatchObject(body('l17_validate_accounts_clear').spec.grok as PolicySpec)
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.disable')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
  })

  it('types both account lists by id while their candidates were not loaded (l17_m31_m3_list_account_options_unavailable)', async () => {
    const { wrapper } = mountPanel(withL17('l17_m31_m3_list_account_options_unavailable'))
    await flushPromises()
    expect(card(wrapper).get('[data-test="policy-warnings"]').text()).toContain('admin.cacheSettings.warnings.account_options_unavailable')
    for (const name of ['account_ids', ACCOUNTS]) {
      expect(field(wrapper, name).find('select').exists(), name).toBe(false)
      expect(field(wrapper, name).get('input').attributes('disabled'), name).toBeUndefined()
    }
  })

  it('locks all three at their default in a build without the Grok fixes (l17_m31_m3_list_on)', async () => {
    const { wrapper } = mountPanel('l17_m31_m3_list_on')
    await flushPromises()
    for (const name of FIELDS) {
      expect(field(wrapper, name).text(), name).toContain('admin.cacheSettings.locked.not_implemented_in_build')
      expect(toggle(wrapper, name).attributes('disabled'), name).toBeDefined()
    }
    expect(field(wrapper, ACCOUNTS).text()).toContain('admin.cacheSettings.locked.not_implemented_in_build')
    expect(field(wrapper, ACCOUNTS).get('select').attributes('disabled')).toBeDefined()
    expect(field(wrapper, ACCOUNTS).get('input').attributes('disabled')).toBeDefined()
    // So neither request of l17_dormant_validate_unsupported nor of l17_dormant_put_400_unsupported can be built here.
    expect(data<ValidateResult>('l17_dormant_validate_unsupported').errors).toEqual([{ field: 'grok.compact_identity', code: 'unsupported_in_build' }])
    expect(toWriteFailure(rejection('l17_dormant_put_400_unsupported'))).toMatchObject({ errors: [{ field: 'grok.chat_body_key', code: 'unsupported_in_build' }] })
    // The rest of the card stays editable.
    expect(field(wrapper, 'max_inflight_per_key').get('input').attributes('disabled')).toBeUndefined()
  })

  it('names the build as the blocker of a switch stored on, and leaves it free to turn off (l17_dormant_list_db_enabled)', async () => {
    const { wrapper } = mountPanel('l17_dormant_list_db_enabled')
    await flushPromises()
    expect(card(wrapper).attributes('data-state')).toBe('blocked')
    expect(card(wrapper).get('[data-test="policy-blockers"]').text()).toContain('admin.cacheSettings.blockers.not_implemented_in_build')
    expect(toggle(wrapper, 'grok.chat_body_key').attributes('aria-checked')).toBe('true')
    expect(toggle(wrapper, 'grok.chat_body_key').attributes('disabled')).toBeUndefined()
    expect(toggle(wrapper, 'grok.compact_identity').attributes('disabled')).toBeDefined()
    // The stored account deviates from the default, so it can be removed.
    expect(field(wrapper, ACCOUNTS).get('li button').attributes('disabled')).toBeUndefined()
  })

  it('shows a Grok switch alone as masked while the master switch is off (l17_list_grok_only_master_off)', async () => {
    const { wrapper } = mountPanel('l17_list_grok_only_master_off')
    await flushPromises()
    expect(card(wrapper).attributes('data-state')).toBe('masked_by_master')
    expect(toggle(wrapper, 'grok.chat_body_key').attributes('aria-checked')).toBe('true')
    expect(toggle(wrapper, 'grok.compact_identity').attributes('aria-checked')).toBe('false')
    for (const name of FIELDS) expect(toggle(wrapper, name).attributes('disabled'), name).toBeUndefined()
  })
})
