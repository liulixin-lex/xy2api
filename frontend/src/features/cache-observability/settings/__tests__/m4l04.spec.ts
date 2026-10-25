// T2.2 (L-04): two fields added to cache_anthropic_identity_policy on the generic card, driven by F's
// m4l04 samples (00-shared/cache_policies_contract/m4l04 v1.0, README §1 and §5). metadata_rollout_percent
// (0..100%, at most rollout_percent) and max_inflight_per_anchor (1..16, default 2) render from the
// schema; in this build only metadata_group_ids keeps not_implemented_in_build (L-03 is still missing).
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

const files = import.meta.glob('./contract/m4l04/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/m4l04/', '').replace('.json', ''))
function m4l04(name: string): ContractSample {
  const found = files[`./contract/m4l04/${name}.json`]
  if (!found) throw new Error(`unknown m4l04 sample ${name}`)
  return structuredClone(found)
}
const data = <T>(name: string) => m4l04(name).response.data as T
const body = (name: string) => m4l04(name).request.body as WriteBody

const KEY = 'anthropic_identity'
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
  const deps = { api, loadGroups: vi.fn().mockResolvedValue(structuredClone(GROUPS)), now: () => new Date('2026-10-25T00:00:00Z'), later: vi.fn() }
  const wrapper = mount(CacheSettingsPanel, { props: { deps }, global: { stubs } })
  const state = (wrapper.vm as unknown as { state: PoliciesState }).state
  return { wrapper, api, state }
}

type Wrapper = ReturnType<typeof mountPanel>['wrapper']
const field = (wrapper: Wrapper, key: string, name: string) => wrapper.get(`[data-test="field-${key}.${name}"]`)
const control = (wrapper: Wrapper, key: string, name: string) => field(wrapper, key, name).find('select, [role="switch"], input')

async function setNumber(wrapper: Wrapper, name: string, value: string) {
  const input = field(wrapper, KEY, name).get('input')
  await input.setValue(value)
  await input.trigger('change')
}

function at(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

describe('anthropic_identity in the T2.2 build (m4l04 samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the two fields and the new error', () => {
    expect(NAMES.sort()).toEqual([
      'm4l04_list_t22',
      'm4l04_put_anchor_enable_200',
      'm4l04_validate_max_inflight_anchor_raise',
      'm4l04_validate_metadata_dormant',
      'm4l04_validate_metadata_rollout_exceeds',
    ])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const name of ['metadata_rollout_percent', 'max_inflight_per_anchor']) {
        for (const part of ['label', 'description', 'risk']) {
          expect(typeof at(root, `admin.cacheSettings.fields.${KEY}.${name}.${part}`), `${lang} ${name} ${part}`).toBe('string')
        }
      }
      // No internal code such as L-03 in the copy (m-2): the note says what is missing instead.
      expect(at(root, `admin.cacheSettings.fields.${KEY}.metadata_rollout_percent.description`), lang).toMatch(lang === 'zh' ? /尚未实现时不生效/ : /until metadata generation is implemented/)
      expect(typeof at(root, 'admin.cacheSettings.errors.exceeds_rollout_percent'), `${lang} error`).toBe('string')
    }
  })

  it('renders both fields from the schema, keeps the metadata groups locked and the anchors editable (m4l04_list_t22)', async () => {
    const { wrapper } = mountPanel('m4l04_list_t22')
    await flushPromises()
    const share = field(wrapper, KEY, 'metadata_rollout_percent').get('input')
    expect(share.attributes()).toMatchObject({ min: '0', max: '100' })
    expect((share.element as HTMLInputElement).value).toBe('0')
    const cap = field(wrapper, KEY, 'max_inflight_per_anchor').get('input')
    expect(cap.attributes()).toMatchObject({ min: '1', max: '16' })
    expect((cap.element as HTMLInputElement).value).toBe('2')
    // L-03 is still missing, so the request of m4l04_validate_metadata_dormant cannot be built here.
    expect(m4l04('m4l04_validate_metadata_dormant').response.data.errors).toEqual([{ field: 'metadata_group_ids', code: 'unsupported_in_build' }])
    expect(field(wrapper, KEY, 'metadata_group_ids').text()).toContain('admin.cacheSettings.locked.not_implemented_in_build')
    expect(control(wrapper, KEY, 'metadata_group_ids').attributes('disabled')).toBeDefined()
    expect(control(wrapper, KEY, 'anchor_group_ids').attributes('disabled')).toBeUndefined()
    expect(control(wrapper, KEY, 'gemini_anchor').attributes('disabled')).toBeUndefined()
    for (const name of ['keep_sticky_on_busy.enabled', 'keep_sticky_on_busy.group_ids']) {
      expect(control(wrapper, 'routing', name).attributes('disabled'), name).toBeDefined()
    }
  })

  it('stops a metadata share above the rollout share before the preview, the rule of m4l04_validate_metadata_rollout_exceeds', async () => {
    expect(data<ValidateResult>('m4l04_validate_metadata_rollout_exceeds').errors).toEqual([
      { field: 'metadata_rollout_percent', code: 'exceeds_rollout_percent', limit: { max: 50 } },
    ])
    const { wrapper, api } = mountPanel('m4l04_list_t22')
    await flushPromises()
    await setNumber(wrapper, 'metadata_rollout_percent', String((body('m4l04_validate_metadata_rollout_exceeds').spec as Record<string, number>).metadata_rollout_percent))
    expect(field(wrapper, KEY, 'metadata_rollout_percent').text()).toContain('admin.cacheSettings.errors.exceeds_rollout_percent:{"limit":50}')
    expect(wrapper.get(`[data-test="preview-${KEY}"]`).attributes('disabled')).toBeDefined()
    expect(api.validatePolicy).not.toHaveBeenCalled()
    // Within the rollout share the field is fine.
    await setNumber(wrapper, 'metadata_rollout_percent', '50')
    expect(field(wrapper, KEY, 'metadata_rollout_percent').text()).not.toContain('admin.cacheSettings.errors.exceeds_rollout_percent')
  })

  it('asks for a reason and a check before raising the in-flight cap, and writes the sample body (m4l04_validate_max_inflight_anchor_raise)', async () => {
    const { wrapper, api } = mountPanel('m4l04_list_t22', data<ValidateResult>('m4l04_validate_max_inflight_anchor_raise'))
    await flushPromises()
    await setNumber(wrapper, 'max_inflight_per_anchor', '4')
    await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
    await flushPromises()
    const sample = body('m4l04_validate_max_inflight_anchor_raise')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    // The answer of an anchor write (m4l04_put_anchor_enable_200) still marks the metadata groups.
    const saved = data<PolicyEntry>('m4l04_put_anchor_enable_200')
    expect(saved.schema.find((f) => f.field === 'metadata_group_ids')?.not_implemented_in_build).toBe(true)
    api.putPolicy.mockResolvedValueOnce(saved)
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, sample])
    expect(control(wrapper, KEY, 'metadata_group_ids').attributes('disabled')).toBeDefined()
  })
})
