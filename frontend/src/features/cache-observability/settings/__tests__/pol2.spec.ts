// FX-POL-2: a stale policy snapshot blocks the behaviour keys (00-shared/cache_policies_contract/pol2 v1.0,
// README §1 and §2). Only the blocker value stale_snapshot is new; the card shows it with the blocked
// state, and the confirmation names it among the preview's blockers.
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
import type { PoliciesResponse, ValidateResult } from '../types'
import type { ContractSample } from './contract'

const files = import.meta.glob('./contract/pol2/*.json', { eager: true, import: 'default' }) as Record<string, ContractSample>
const NAMES = Object.keys(files).map((p) => p.replace('./contract/pol2/', '').replace('.json', ''))
function data<T>(name: string): T {
  const found = files[`./contract/pol2/${name}.json`]
  if (!found) throw new Error(`unknown pol2 sample ${name}`)
  return structuredClone(found).response.data as T
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

function mountPanel(list: string, validate?: ValidateResult) {
  const api = {
    listPolicies: vi.fn().mockResolvedValue(data<PoliciesResponse>(list)),
    getPolicy: vi.fn(),
    validatePolicy: vi.fn().mockResolvedValue(validate),
    putPolicy: vi.fn(),
    listHistory: vi.fn().mockResolvedValue({ items: [], history_complete: true, history_incomplete_reason: null }),
  }
  const deps = { api, loadGroups: vi.fn().mockResolvedValue([]), now: () => new Date('2026-10-25T05:00:00Z'), later: vi.fn() }
  return { wrapper: mount(CacheSettingsPanel, { props: { deps }, global: { stubs } }), api }
}

function at(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], root)
}

describe('stale policy snapshot (pol2 samples)', () => {
  beforeEach(() => vi.clearAllMocks())

  it('has the samples, and zh and en copy for the blocker and its check line', () => {
    expect(NAMES.sort()).toEqual(['pol2_list_killed_and_stale', 'pol2_m3_list_on', 'pol2_m3_validate_enable'])
    for (const [lang, root] of [['zh', zh], ['en', en]] as const) {
      for (const group of ['blockers', 'acks']) expect(typeof at(root, `admin.cacheSettings.${group}.stale_snapshot`), `${lang} ${group}`).toBe('string')
    }
    expect(at(zh, 'admin.cacheSettings.blockers.stale_snapshot')).toContain('策略快照已过期')
  })

  it('shows the stale snapshot on the blocked behaviour card only (pol2_m3_list_on)', async () => {
    const { wrapper } = mountPanel('pol2_m3_list_on')
    await flushPromises()
    const card = wrapper.get('[data-test="policy-upstream_identity"]')
    expect(card.attributes('data-state')).toBe('blocked')
    expect(card.get('[data-test="policy-blockers"]').text()).toContain('admin.cacheSettings.blockers.stale_snapshot')
    expect(wrapper.get('[data-test="policy-observability"]').find('[data-test="policy-blockers"]').exists()).toBe(false)
  })

  it('names the stale snapshot under the kill switch without repeating the switch on the cards (pol2_list_killed_and_stale)', async () => {
    const { wrapper } = mountPanel('pol2_list_killed_and_stale')
    await flushPromises()
    const card = wrapper.get('[data-test="policy-upstream_identity"]')
    expect(card.attributes('data-state')).toBe('killed')
    const blockers = card.get('[data-test="policy-blockers"]').text()
    expect(blockers).toContain('admin.cacheSettings.blockers.stale_snapshot')
    expect(blockers).not.toContain('admin.cacheSettings.blockers.kill_switch')
  })

  it('lists the stale snapshot among the checks of a write that would stay blocked (pol2_m3_validate_enable)', async () => {
    // Values from pol2_m3_validate_enable: the preview stays blocked by stale_snapshot.
    const { wrapper } = mountPanel('pol2_m3_list_on', { ...data<ValidateResult>('pol2_m3_validate_enable'), current_rev: 4 })
    await flushPromises()
    const input = wrapper.get('[data-test="field-upstream_identity.rollout_percent"]').get('input')
    await input.setValue('60')
    await input.trigger('change')
    await wrapper.get('[data-test="preview-upstream_identity"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="confirm-check"]').text()).toContain('admin.cacheSettings.acks.stale_snapshot')
    expect(wrapper.get('[data-test="confirm-check"]').text()).toContain('admin.cacheSettings.acks.observability_off')
  })
})
