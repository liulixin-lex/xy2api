import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed } from 'vue'
import GroupQualityGateField from '../GroupQualityGateField.vue'
import type { GroupQualityGatePolicies } from '@/types/groupQualityGate'

const mocks = vi.hoisted(() => ({ getPolicies: vi.fn(), updatePolicy: vi.fn(), showWarning: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { groupQualityGate: { getPolicies: mocks.getPolicies, updatePolicy: mocks.updatePolicy } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showWarning: mocks.showWarning }) }))
vi.mock('@/composables/useAdminPermission', () => ({ useAdminPermission: () => ({ canView: computed(() => true) }) }))

// 现网 v2 条目，GET 已按继承规则填好 v3。
const policies = (version = 4): GroupQualityGatePolicies => ({
  version,
  groups: {
    '53': {
      enabled: true, max_error_rate: 0.08, max_warm_ttft_p90_ms: 20000, fast_warm_ttft_p90_ms: 10000, pinned_account_ids: [7],
      v3: { enabled: true, max_ttft_p90_seconds: 15, max_error_rate: 0.1, min_cache_keep_rate: 0.5, strict: false }
    }
  }
})

const ToggleStub = {
  props: ['modelValue', 'disabled'],
  emits: ['update:modelValue'],
  template: '<button type="button" :disabled="disabled" :data-on="String(modelValue)" @click="$emit(\'update:modelValue\', !modelValue)" />'
}

const mountField = () => mount(GroupQualityGateField, { props: { groupId: 53 }, global: { stubs: { Toggle: ToggleStub } } })
type Field = { validate: () => boolean; save: (groupId: number) => Promise<boolean> }

describe('GroupQualityGateField', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getPolicies.mockResolvedValue(policies())
    mocks.updatePolicy.mockImplementation(async (_id: number, request: { policy: unknown }) => ({ version: 5, policy: request.policy }))
  })

  it('edits only the v3 fields and sends the v2 fields back untouched', async () => {
    const w = mountField(); await flushPromises()
    const fields = w.get('[data-testid="quality-gate-fields"]')
    expect(fields.findAll('input').map(input => input.attributes('data-testid'))).toEqual([
      'quality-gate-max_ttft_p90_seconds', 'quality-gate-max_error_rate_percent', 'quality-gate-min_cache_keep_rate_percent'
    ])
    expect(w.text()).not.toContain('pinned')

    await w.get('[data-testid="quality-gate-max_ttft_p90_seconds"]').setValue('10')
    await w.get('[data-testid="quality-gate-max_error_rate_percent"]').setValue('8')
    await w.get('[data-testid="quality-gate-strict"]').trigger('click')
    const field = w.vm as unknown as Field
    expect(field.validate()).toBe(true)
    expect(await field.save(53)).toBe(true)
    expect(mocks.updatePolicy).toHaveBeenCalledWith(53, {
      expected_version: 4,
      policy: {
        enabled: true, max_error_rate: 0.08, max_warm_ttft_p90_ms: 20000, fast_warm_ttft_p90_ms: 10000, pinned_account_ids: [7],
        v3: { enabled: true, max_ttft_p90_seconds: 10, max_error_rate: 0.08, min_cache_keep_rate: 0.5, strict: true }
      }
    })
    expect(w.emitted('saved')?.[0][1]).toBe(5)
  })

  it('does not save without changes and blocks out-of-range values', async () => {
    const w = mountField(); await flushPromises()
    const field = w.vm as unknown as Field
    expect(await field.save(53)).toBe(true)
    expect(mocks.updatePolicy).not.toHaveBeenCalled()

    await w.get('[data-testid="quality-gate-min_cache_keep_rate_percent"]').setValue('75')
    expect(field.validate()).toBe(false)
    await w.vm.$nextTick()
    expect(w.text()).toContain('admin.groupQualityGate.minCacheKeepRange')
  })

  it('re-reads the version after a 409 and keeps the edited values', async () => {
    const w = mountField(); await flushPromises()
    mocks.updatePolicy.mockRejectedValueOnce({ status: 409, message: 'version conflict' })
    mocks.getPolicies.mockResolvedValue(policies(6))
    await w.get('[data-testid="quality-gate-max_ttft_p90_seconds"]').setValue('20')
    const field = w.vm as unknown as Field
    expect(await field.save(53)).toBe(false)
    expect(mocks.showWarning).toHaveBeenCalledWith('admin.groupQualityGate.groupSavedGateConflict')
    expect((w.get('[data-testid="quality-gate-max_ttft_p90_seconds"]').element as HTMLInputElement).value).toBe('20')

    expect(await field.save(53)).toBe(true)
    expect(mocks.updatePolicy).toHaveBeenLastCalledWith(53, expect.objectContaining({ expected_version: 6 }))
  })

  it('turning the gate off restores the loaded numbers and saves enabled=false only in v3', async () => {
    const w = mountField(); await flushPromises()
    await w.get('[data-testid="quality-gate-max_ttft_p90_seconds"]').setValue('99')
    await w.get('[data-testid="quality-gate-toggle"]').trigger('click')
    expect(w.find('[data-testid="quality-gate-fields"]').exists()).toBe(false)
    const field = w.vm as unknown as Field
    expect(field.validate()).toBe(true)
    await field.save(53)
    const { policy } = mocks.updatePolicy.mock.calls[0][1]
    expect(policy.enabled).toBe(true)
    expect(policy.v3).toEqual({ enabled: false, max_ttft_p90_seconds: 15, max_error_rate: 0.1, min_cache_keep_rate: 0.5, strict: false })
  })
})
