// T3.1a (L-09a): the previous host preference on cache_routing_policy, driven by F's l09a samples
// (00-shared/cache_policies_contract/l09a v1.0, README §1, §2 and §4). affinity_mode accepts prev_host
// when the build has l09a (a capability field: without it the X-B1 lock keeps it at score);
// prev_host+quality stays refused by the server. affinity_rollout_percent (0..100%) follows it. The
// blocker static_aware_routing_disabled names the static cache aware routing it depends on.
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

const files = import.meta.glob('./contract/l09a/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/l09a/', '').replace('.json', ''))
function l09a(name: string): ContractSample {
  const found = files[`./contract/l09a/${name}.json`]
  if (!found) throw new Error(`unknown l09a sample ${name}`)
  return structuredClone(found)
}
const data = <T>(name: string) => l09a(name).response.data as T
const body = (name: string) => l09a(name).request.body as WriteBody

const KEY = 'routing'
const MODE = 'affinity_mode'
const SHARE = 'affinity_rollout_percent'

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
const picker = (wrapper: Wrapper) => field(wrapper, MODE).get('select')
const entry = (list: PoliciesResponse) => list.policies.find((p) => p.key === KEY) as PolicyEntry
/** The same stored value as a build with l09a serves it: the marks of the build without it removed. */
function withL09a(name: string): PoliciesResponse {
  const list = data<PoliciesResponse>(name)
  for (const f of entry(list).schema) delete f.not_implemented_in_build
  return list
}

async function setShare(wrapper: Wrapper, value: string) {
  const input = field(wrapper, SHARE).get('input')
  await input.setValue(value)
  await input.trigger('change')
}

async function preview(wrapper: Wrapper) {
  await wrapper.get(`[data-test="preview-${KEY}"]`).trigger('click')
  await flushPromises()
}

function at(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

describe('routing previous host preference (l09a samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the three modes, the share and the blocker', () => {
    expect(NAMES.sort()).toEqual([
      'l09a_dormant_list_db_enabled',
      'l09a_dormant_validate_unsupported',
      'l09a_list_prev_host_on',
      'l09a_list_prev_host_static_off',
      'l09a_m4l07_list_t25',
      'l09a_m4l07_m4_list_default',
      'l09a_put_200',
      'l09a_validate_disable',
      'l09a_validate_enable',
      'l09a_validate_quality_unsupported',
    ])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const option of ['score', 'prev_host', 'prev_host_plus_quality']) {
        expect(typeof at(root, `admin.cacheSettings.options.${option}`), `${lang} ${option}`).toBe('string')
      }
      // The option the server still refuses says so in its name.
      expect(at(root, 'admin.cacheSettings.options.prev_host_plus_quality'), lang).toMatch(lang === 'zh' ? /暂不可用/ : /not available yet/)
      for (const part of ['label', 'description', 'risk']) {
        expect(typeof at(root, `admin.cacheSettings.fields.${KEY}.${SHARE}.${part}`), `${lang} ${part}`).toBe('string')
      }
      expect(at(root, `admin.cacheSettings.fields.${KEY}.${MODE}.description`), lang).toMatch(lang === 'zh' ? /不会增加重试/ : /never adds retries/)
      for (const group of ['blockers', 'acks']) {
        expect(typeof at(root, `admin.cacheSettings.${group}.static_aware_routing_disabled`), `${lang} ${group}`).toBe('string')
      }
    }
  })

  it('shows the previous host preference with its share right after the mode (l09a_list_prev_host_on)', async () => {
    const { wrapper } = mountPanel('l09a_list_prev_host_on')
    await flushPromises()
    expect(picker(wrapper).findAll('option').map((o) => o.attributes('value'))).toEqual(['score', 'prev_host', 'prev_host+quality'])
    expect((picker(wrapper).element as HTMLSelectElement).value).toBe('prev_host')
    expect(picker(wrapper).attributes('disabled')).toBeUndefined()
    const rows = card(wrapper).findAll(`[data-test^="field-${KEY}."]`).map((row) => row.attributes('data-test'))
    expect(rows.indexOf(`field-${KEY}.${SHARE}`)).toBe(rows.indexOf(`field-${KEY}.${MODE}`) + 1)
    const share = field(wrapper, SHARE).get('input')
    expect(share.attributes()).toMatchObject({ min: '0', max: '100' })
    expect((share.element as HTMLInputElement).value).toBe('20')
    expect(card(wrapper).attributes('data-state')).toBe('on')
  })

  it('names the static cache aware routing it depends on (l09a_list_prev_host_static_off)', async () => {
    const { wrapper } = mountPanel('l09a_list_prev_host_static_off')
    await flushPromises()
    expect(card(wrapper).attributes('data-state')).toBe('blocked')
    expect(card(wrapper).get('[data-test="policy-blockers"]').text()).toContain('admin.cacheSettings.blockers.static_aware_routing_disabled')
  })

  it('turns it on as an enable that asks for a reason, a check and the key name, and writes the sample body (l09a_validate_enable, l09a_put_200)', async () => {
    const { wrapper, api } = mountPanel(withL09a('l09a_m4l07_list_t25'), data<ValidateResult>('l09a_validate_enable'))
    await flushPromises()
    await picker(wrapper).setValue('prev_host')
    await setShare(wrapper, '20')
    await preview(wrapper)
    const sample = body('l09a_validate_enable')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    expect(wrapper.get('[data-test="dialog-risk"]').classes()).toContain('badge-danger')
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.enable')
    await wrapper.get('[data-test="reason"]').setValue(sample.reason as string)
    await wrapper.get('[data-test="confirm-checkbox"]').setValue(true)
    await wrapper.get('[data-test="confirm-typed"]').setValue('cache_routing_policy')
    // Keeping sticky keeps the key on, but the preview still names what the preference waits for.
    expect(wrapper.get('[data-test="confirm-check"]').text()).toContain('admin.cacheSettings.acks.static_aware_routing_disabled')
    api.putPolicy.mockResolvedValueOnce(data<PolicyEntry>('l09a_put_200'))
    await wrapper.get('[data-test="confirm-submit"]').trigger('click')
    await flushPromises()
    expect(api.putPolicy.mock.calls[0]).toEqual([KEY, sample])
  })

  it('shows the refusal of the previous host plus quality at the mode (l09a_validate_quality_unsupported)', async () => {
    const { wrapper, api } = mountPanel(withL09a('l09a_m4l07_list_t25'), data<ValidateResult>('l09a_validate_quality_unsupported'))
    await flushPromises()
    await picker(wrapper).setValue('prev_host+quality')
    await preview(wrapper)
    const sample = body('l09a_validate_quality_unsupported')
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: sample.expected_rev, spec: sample.spec })
    expect(field(wrapper, MODE).text()).toContain('admin.cacheSettings.errors.unsupported_in_build')
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeDefined()
  })

  it('goes back to the cache score as a disable without asking (l09a_validate_disable)', async () => {
    const { wrapper, api } = mountPanel('l09a_list_prev_host_on', { ...data<ValidateResult>('l09a_validate_disable'), current_rev: 1 })
    await flushPromises()
    await picker(wrapper).setValue('score')
    await preview(wrapper)
    expect(api.validatePolicy).toHaveBeenCalledWith(KEY, { expected_rev: 1, spec: body('l09a_validate_disable').spec })
    expect(wrapper.get('[data-test="direction"]').text()).toContain('admin.cacheSettings.direction.disable')
    expect(wrapper.find('[data-test="confirm-checkbox"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="confirm-typed"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="confirm-submit"]').attributes('disabled')).toBeUndefined()
  })

  it('locks the mode at the cache score in a build without it, the share stays editable (l09a_m4l07_m4_list_default)', async () => {
    const { wrapper } = mountPanel('l09a_m4l07_m4_list_default')
    await flushPromises()
    expect(field(wrapper, MODE).text()).toContain('admin.cacheSettings.locked.not_implemented_in_build')
    expect(picker(wrapper).attributes('disabled')).toBeDefined()
    expect(field(wrapper, SHARE).get('input').attributes('disabled')).toBeUndefined()
    // So the request of l09a_dormant_validate_unsupported cannot be built here.
    expect(data<ValidateResult>('l09a_dormant_validate_unsupported').errors).toEqual([{ field: MODE, code: 'unsupported_in_build' }])
  })

  it('lists both blockers in the server order, and leaves a stored preference free to go back (l09a_dormant_list_db_enabled)', async () => {
    const { wrapper } = mountPanel('l09a_dormant_list_db_enabled')
    await flushPromises()
    expect(card(wrapper).attributes('data-state')).toBe('blocked')
    const blockers = card(wrapper).get('[data-test="policy-blockers"]').text()
    const statik = blockers.indexOf('admin.cacheSettings.blockers.static_aware_routing_disabled')
    expect(statik).toBeGreaterThanOrEqual(0)
    expect(blockers.indexOf('admin.cacheSettings.blockers.not_implemented_in_build')).toBeGreaterThan(statik)
    expect((picker(wrapper).element as HTMLSelectElement).value).toBe('prev_host')
    expect(picker(wrapper).attributes('disabled')).toBeUndefined()
  })
})
