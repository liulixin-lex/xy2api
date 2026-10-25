// M3.1: two additions to cache_upstream_identity_policy on the generic card, driven by F's m31
// samples (00-shared/cache_policies_contract/m31 v1.0, README §1): the max_inflight_per_key field
// (int 1..16, default 2; raising is enable, lowering is disable) and the l01_all_accounts_unverified
// hint (mode uuid_fill with every eligible account). The card renders both from the server; this
// build adds their copy.
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
import type { PoliciesResponse, PolicyEntry, ValidateResult, WriteBody } from '../types'
import type { PoliciesState } from '../usePolicies'
import type { ContractSample } from './contract'

const files = import.meta.glob('./contract/m31/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/m31/', '').replace('.json', ''))
function m31(name: string): ContractSample {
  const found = files[`./contract/m31/${name}.json`]
  if (!found) throw new Error(`unknown m31 sample ${name}`)
  return structuredClone(found)
}
const data = <T>(name: string) => m31(name).response.data as T
const body = (name: string) => m31(name).request.body as WriteBody

const KEY = 'upstream_identity'
const FIELD = 'max_inflight_per_key'

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

function mountPanel(list: string, validate?: ValidateResult) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(data<PoliciesResponse>(list)),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue({ items: [], history_complete: true, history_incomplete_reason: null }),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-24T19:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']
const field = (wrapper: Wrapper) => wrapper.get(`[data-test="field-${KEY}.${FIELD}"]`)

async function setCap(wrapper: Wrapper, value: string) {
  const input = field(wrapper).get('input')
  await input.setValue(value)
  await input.trigger('change')
}

function at(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

describe('upstream_identity in the M3.1 build (m31 samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the field, the hint and its check line', () => {
    expect(NAMES.sort()).toEqual(['m31_list_all_accounts_unverified', 'm31_m3_list_on', 'm31_put_200_max_inflight', 'm31_validate_max_inflight_out_of_range', 'm31_validate_max_inflight_raise'])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const part of ['label', 'description', 'risk']) {
        expect(typeof at(root, `admin.cacheSettings.fields.${KEY}.${FIELD}.${part}`), `${lang} ${part}`).toBe('string')
      }
      expect(typeof at(root, 'admin.cacheSettings.warnings.l01_all_accounts_unverified'), `${lang} warning`).toBe('string')
      expect(typeof at(root, 'admin.cacheSettings.acks.l01_all_accounts_unverified'), `${lang} ack`).toBe('string')
    }
  })

  it('renders the cap from the schema: 1 to 16, stored 2 (m31_m3_list_on)', async () => {
    const entry = data<PoliciesResponse>('m31_m3_list_on').policies.find((p) => p.key === KEY) as PolicyEntry
    expect(entry.schema.at(-1)).toEqual({ field: FIELD, type: 'int', min: 1, max: 16 })
    const { wrapper } = mountPanel('m31_m3_list_on')
    await flushPromises()
    const input = field(wrapper).get('input')
    expect(input.attributes()).toMatchObject({ min: '1', max: '16' })
    expect((input.element as HTMLInputElement).value).toBe('2')
    expect(field(wrapper).text()).toContain(`admin.cacheSettings.fields.${KEY}.${FIELD}.label`)
  })

  it('shows the hint when the fill covers every eligible account (m31_list_all_accounts_unverified)', async () => {
    const { wrapper } = mountPanel('m31_list_all_accounts_unverified')
    await flushPromises()
    expect(wrapper.get(`[data-test="policy-${KEY}"] [data-test="policy-warnings"]`).text()).toContain('admin.cacheSettings.warnings.l01_all_accounts_unverified')
  })

  it('asks for a reason and a check before raising the cap, then writes the sample body (m31_validate_max_inflight_raise)', async () => {
    const { wrapper, api } = mountPanel('m31_m3_list_on', data<ValidateResult>('m31_validate_max_inflight_raise'))
    await flushPromises()
    await setCap(wrapper, '4')
    await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
    await flushPromises()
    const sample = body('m31_validate_max_inflight_raise')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.enable')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    api.putPolicy.mockResolvedValueOnce(data<PolicyEntry>('m31_put_200_max_inflight'))
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, sample])
  })

  it('stops a cap above 16 before the preview, the rule of m31_validate_max_inflight_out_of_range', async () => {
    const refused = data<ValidateResult>('m31_validate_max_inflight_out_of_range')
    expect(refused.errors).toEqual([{ field: FIELD, code: 'out_of_range', limit: { min: 1, max: 16 } }])
    const { wrapper, api } = mountPanel('m31_m3_list_on')
    await flushPromises()
    await setCap(wrapper, String((body('m31_validate_max_inflight_out_of_range').spec as Record<string, number>)[FIELD]))
    expect(field(wrapper).text()).toContain('admin.cacheSettings.errors.out_of_range')
    expect(wrapper.get(`[data-test="preview-${KEY}"]`).attributes('disabled')).toBeDefined()
    expect(api.validatePolicy).not.toHaveBeenCalled()
  })
})
