// T2.5 (L-07): five limits added under keep_sticky_on_busy of cache_routing_policy, driven by F's m4l07
// samples (00-shared/cache_policies_contract/m4l07 v1.0, README §1 and §5; DESIGN §6 A1-A4 and H1).
// They render from the schema; the server judges direction (a longer health look-back is a disable).
// The new blocker guard_tripped and warning share_group_no_effect come with their copy.
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

const files = import.meta.glob('./contract/m4l07/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/m4l07/', '').replace('.json', ''))
function m4l07(name: string): ContractSample {
  const found = files[`./contract/m4l07/${name}.json`]
  if (!found) throw new Error(`unknown m4l07 sample ${name}`)
  return structuredClone(found)
}
const data = <T>(name: string) => m4l07(name).response.data as T
const body = (name: string) => m4l07(name).request.body as WriteBody

const KEY = 'routing'
const LIMITS = ['max_consecutive_busy', 'max_hold_seconds', 'owner_busy_skip_per_min_cap', 'group_keep_ratio_pct', 'health_lookback_seconds']
const GROUPS = [
  { id: 31, name: 'claude-pool-a', platform: 'anthropic' },
  { id: 32, name: 'claude-pool-b', platform: 'anthropic' },
  { id: 77, name: 'claude-standby', platform: 'anthropic' },
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

function mountPanel(list: string, validate?: ValidateResult) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(data<PoliciesResponse>(list)),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue({ items: [], history_complete: true, history_incomplete_reason: null }),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue(structuredClone(GROUPS)), now: () => new Date('2026-10-25T04:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']
const path = (name: string) => `keep_sticky_on_busy.${name}`
const field = (wrapper: Wrapper, name: string) => wrapper.get(`[data-test="field-${KEY}.${path(name)}"]`)
const submit = (wrapper: Wrapper) => wrapper.get('[data-test="confirm-submit"]')

async function setNumber(wrapper: Wrapper, name: string, value: string) {
  const input = field(wrapper, name).get('input')
  await input.setValue(value)
  await input.trigger('change')
}

async function preview(wrapper: Wrapper) {
  await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
  await flushPromises()
}

function at(root: unknown, keyPath: string): unknown {
  return keyPath.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

function storedRouting(list: string): PolicySpec {
  const entry = data<PoliciesResponse>(list).policies.find((p) => p.key === KEY) as PolicyEntry
  return entry.stored!.spec
}

/** The card sends the nested object whole: the sample's fields as sent, every other one at its stored value. */
function sameNested(sent: { expected_rev: number; spec: PolicySpec }, sample: { expected_rev: number; spec: PolicySpec }, stored: PolicySpec) {
  expect(sent.expected_rev).toBe(sample.expected_rev)
  expect(Object.keys(sent.spec)).toEqual(['keep_sticky_on_busy'])
  expect(sent.spec.keep_sticky_on_busy).toEqual({ ...(stored.keep_sticky_on_busy as PolicySpec), ...(sample.spec.keep_sticky_on_busy as PolicySpec) })
}

describe('routing in the T2.5 build (m4l07 samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the five limits, the blocker and the warning', () => {
    expect(NAMES.sort()).toEqual([
      'm4l07_list_clipped_static_cap',
      'm4l07_list_guard_tripped',
      'm4l07_list_t25',
      'm4l07_put_400_exceeds_static_cap',
      'm4l07_validate_health_lookback_raise',
      'm4l07_validate_share_group_warning',
    ])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const name of LIMITS) {
        for (const part of ['label', 'description', 'risk']) {
          expect(typeof at(root, `admin.cacheSettings.fields.${KEY}.keep_sticky_on_busy_${name}.${part}`), `${lang} ${name} ${part}`).toBe('string')
        }
      }
      expect(at(root, `admin.cacheSettings.fields.${KEY}.keep_sticky_on_busy_owner_busy_skip_per_min_cap.description`), lang).toMatch(/0/)
      for (const group of ['blockers', 'acks']) expect(typeof at(root, `admin.cacheSettings.${group}.guard_tripped`), `${lang} ${group}`).toBe('string')
      for (const group of ['warnings', 'acks']) expect(typeof at(root, `admin.cacheSettings.${group}.share_group_no_effect`), `${lang} ${group}`).toBe('string')
    }
  })

  it('renders the five limits from the schema with their stored values and cap sources (m4l07_list_t25)', async () => {
    const { wrapper } = mountPanel('m4l07_list_t25')
    await flushPromises()
    const entry = data<PoliciesResponse>('m4l07_list_t25').policies.find((p) => p.key === KEY) as PolicyEntry
    const stored = storedRouting('m4l07_list_t25').keep_sticky_on_busy as Record<string, number>
    for (const name of LIMITS) {
      const schema = entry.schema.find((f) => f.field === path(name))!
      const input = field(wrapper, name).get('input')
      expect(input.attributes(), name).toMatchObject({ min: String(schema.min), max: String(schema.max) })
      expect((input.element as HTMLInputElement).value, name).toBe(String(stored[name]))
      if (schema.cap_source) expect(field(wrapper, name).text(), name).toContain(schema.cap_source)
    }
    // T2.5 implements L-07: the switch and the groups are no longer locked here.
    expect(field(wrapper, 'enabled').get('[role="switch"]').attributes('disabled')).toBeUndefined()
  })

  it('marks a stored limit above a lowered static cap as clipped (m4l07_list_clipped_static_cap)', async () => {
    const { wrapper } = mountPanel('m4l07_list_clipped_static_cap')
    await flushPromises()
    const hold = field(wrapper, 'max_hold_seconds')
    expect(hold.get('input').attributes('max')).toBe('120')
    expect(hold.text()).toContain('admin.cacheSettings.clipped')
    expect(hold.text()).toContain('admin.cacheSettings.clippedRunsAt:{"value":"120"}')
  })

  it('shows the guard pause as a blocker of the blocked card (m4l07_list_guard_tripped)', async () => {
    const { wrapper } = mountPanel('m4l07_list_guard_tripped')
    await flushPromises()
    const card = wrapper.get(`[data-test="policy-${KEY}"]`)
    expect(card.attributes('data-state')).toBe('blocked')
    expect(card.get('[data-test="policy-blockers"]').text()).toContain('admin.cacheSettings.blockers.guard_tripped')
  })

  it('takes a longer health look-back as the disable the server says it is (m4l07_validate_health_lookback_raise)', async () => {
    const { wrapper, api } = mountPanel('m4l07_list_t25', data<ValidateResult>('m4l07_validate_health_lookback_raise'))
    await flushPromises()
    await setNumber(wrapper, 'health_lookback_seconds', '300')
    await preview(wrapper)
    sameNested(api.validatePolicy.mock.calls[0][1], body('m4l07_validate_health_lookback_raise'), storedRouting('m4l07_list_t25'))
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-gray')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    expect(submit(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('names a shared-scheduling group in the confirmation and still writes it (m4l07_validate_share_group_warning)', async () => {
    const { wrapper, api } = mountPanel('m4l07_list_t25', data<ValidateResult>('m4l07_validate_share_group_warning'))
    await flushPromises()
    await field(wrapper, 'group_ids').get('select').setValue('32')
    await preview(wrapper)
    const sample = body('m4l07_validate_share_group_warning')
    const stored = storedRouting('m4l07_list_t25')
    sameNested(api.validatePolicy.mock.calls[0][1], sample, stored)
    expect(wrapper.get('[data-test="validate-warnings"]').text()).toContain('admin.cacheSettings.warnings.share_group_no_effect')
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing_policy')
    api.putPolicy.mockResolvedValueOnce(data<PoliciesResponse>('m4l07_list_t25').policies.find((p) => p.key === KEY) as PolicyEntry)
    await submit(wrapper).trigger('click')
    await flushPromises()
    const put = api.putPolicy.mock.calls[0][1]
    sameNested(put, sample, stored)
    expect({ reason: put.reason, confirm: put.confirm }).toEqual({ reason: sample.reason, confirm: sample.confirm })
  })

  it('stops a limit above its static cap before the preview, the case of m4l07_put_400_exceeds_static_cap', async () => {
    expect(toWriteFailure(m4l07Rejection('m4l07_put_400_exceeds_static_cap'))).toEqual({
      kind: 'invalid',
      errors: [{ field: path('max_consecutive_busy'), code: 'exceeds_static_cap', limit: { max: 5 } }],
    })
    const { wrapper, api } = mountPanel('m4l07_list_t25')
    await flushPromises()
    await setNumber(wrapper, 'max_consecutive_busy', String(((body('m4l07_put_400_exceeds_static_cap').spec.keep_sticky_on_busy as Record<string, number>).max_consecutive_busy)))
    expect(field(wrapper, 'max_consecutive_busy').text()).toContain('admin.cacheSettings.errors.exceeds_static_cap')
    expect(wrapper.get(`[data-test="preview-${KEY}"]`).attributes('disabled')).toBeDefined()
    expect(api.validatePolicy).not.toHaveBeenCalled()
  })
})

/** What `apiClient` rejects with for an error sample (as `rejection` in ./contract). */
function m4l07Rejection(name: string) {
  const s = m4l07(name)
  return { status: s.status, code: s.response.code, reason: s.response.reason, message: s.response.message, metadata: s.response.metadata }
}
