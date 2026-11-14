import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { SchedGroupPolicy } from '@/types/schedQuality'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import GroupSchedPolicyField from '../GroupSchedPolicyField.vue'

const api = vi.hoisted(() => ({ getGroupPolicy: vi.fn(), updateGroupPolicy: vi.fn(), previewGroupPolicy: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const policy = () => structuredClone(policyFixture) as SchedGroupPolicy
const save = (w: ReturnType<typeof mount>) => (w.vm as unknown as { save: () => Promise<boolean> }).save()
const ttft = (w: ReturnType<typeof mount>) => w.get('input[step="0.1"][min="0"]')
const error = (w: ReturnType<typeof mount>) => w.get('input[step="0.1"][min="5"]')
const cache = (w: ReturnType<typeof mount>) => w.get('input[step="1"][max="100"]')

async function mountField() {
  const w = mount(GroupSchedPolicyField, { props: { groupId: 53 }, global: { stubs: { Select: true } } })
  await flushPromises()
  return w
}

beforeEach(() => {
  api.getGroupPolicy.mockReset().mockResolvedValue(policy())
  api.updateGroupPolicy.mockReset()
  api.previewGroupPolicy.mockReset().mockResolvedValue({
    now: { normal: 5, below: 1 },
    after: { normal: 4, below: 2 },
    changed: [{ account_id: 7, name: 'acc-7', model: 'gpt-6.1-sol', dims: ['ttfo'] }]
  })
})
afterEach(() => { vi.useRealTimers() })

describe('GroupSchedPolicyField', () => {
  it('keeps the API precision of untouched lines: an unchanged block sends no PUT, a typed field goes as typed', async () => {
    const p = policy()
    Object.assign(p.policy, { min_cache_ratio: 0.875, max_error_rate: 0.0625 })
    api.getGroupPolicy.mockResolvedValue(p)
    const w = await mountField()
    expect((cache(w).element as HTMLInputElement).value).toBe('88')
    expect((error(w).element as HTMLInputElement).value).toBe('6.25')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).not.toHaveBeenCalled()

    api.updateGroupPolicy.mockResolvedValueOnce({ ...p, version: 64, policy: { ...p.policy, enabled: false } })
    await w.get('[role="switch"]').trigger('click')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, {
      expected_version: 63,
      policy: { enabled: false, ttft_seconds: 10, max_error_rate: 0.0625, min_cache_ratio: 0.875, takeover_idle_seconds: 900 }
    })

    await cache(w).setValue('88')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, expect.objectContaining({ policy: expect.objectContaining({ min_cache_ratio: 0.88 }) }))
  })

  it('refuses an error line under 5% and a first-output line of 0, takes any first-output line above 0 (FINAL §3)', async () => {
    const w = await mountField()
    await error(w).setValue('4.9')
    expect(error(w).attributes('aria-invalid')).toBe('true')
    expect(error(w).classes()).toContain('input-error')
    await error(w).setValue('5')
    expect(error(w).attributes('aria-invalid')).toBe('false')
    await ttft(w).setValue('0.05')
    expect(ttft(w).attributes('aria-invalid')).toBe('false')
    await ttft(w).setValue('0')
    expect(ttft(w).attributes('aria-invalid')).toBe('true')
    expect((w.vm as unknown as { valid: boolean }).valid).toBe(false)
    expect(await save(w)).toBe(false)
    expect(api.updateGroupPolicy).not.toHaveBeenCalled()
  })

  it('previews edited lines once typing stops: counts now → after and the accounts that would turn substandard', async () => {
    vi.useFakeTimers()
    const w = await mountField()
    expect(w.find('[data-sq-preview]').exists()).toBe(false)

    await ttft(w).setValue('9')
    await ttft(w).setValue('8')
    vi.advanceTimersByTime(400)
    await flushPromises()
    expect(api.previewGroupPolicy.mock.calls).toEqual([[53, {
      expected_version: 63,
      policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 }
    }]])
    const text = w.get('[data-sq-preview]').text().replace(/\s+/g, ' ')
    expect(text).toContain('admin.schedQuality.state.normal 5 → 4')
    expect(text).toContain('admin.schedQuality.state.substandard 1 → 2')
    expect(w.findAll('[data-sq-preview-changed] li > span').map((el) => el.text())).toEqual(['acc-7', 'gpt-6.1-sol', 'admin.schedQuality.state.substandard·admin.schedQuality.dim.ttfo'])

    // Back to the saved value: no preview, no request.
    await ttft(w).setValue('10')
    vi.advanceTimersByTime(400)
    await flushPromises()
    expect(w.find('[data-sq-preview]').exists()).toBe(false)
    expect(api.previewGroupPolicy).toHaveBeenCalledTimes(1)
  })

  it('shows 失败 in place of the preview when the preview request fails', async () => {
    vi.useFakeTimers()
    api.previewGroupPolicy.mockRejectedValueOnce({ status: 500 })
    const w = await mountField()
    await ttft(w).setValue('8')
    vi.advanceTimersByTime(400)
    await flushPromises()
    expect(w.get('[data-sq-preview]').text()).toBe('admin.schedQuality.result.failed')
  })

  it('shows 失败 without reload on a save error other than 409, and clears it on the next good save', async () => {
    const w = await mountField()
    await ttft(w).setValue('12')
    api.updateGroupPolicy.mockRejectedValueOnce({ status: 500, message: 'boom' })
    expect(await save(w)).toBe(false)
    expect(w.get('[data-sq-policy-failed]').text()).toBe('admin.schedQuality.result.failed')
    expect(w.find('[data-sq-policy-reload]').exists()).toBe(false)

    api.updateGroupPolicy.mockResolvedValueOnce({ ...policy(), version: 64 })
    expect(await save(w)).toBe(true)
    expect(w.find('[data-sq-policy-failed]').exists()).toBe(false)
  })

  it('saves with the version, and on a conflict shows failed + reload, which reads the server values back', async () => {
    const w = await mountField()
    await ttft(w).setValue('12.5')
    api.updateGroupPolicy.mockRejectedValueOnce({ status: 409, message: 'conflict' })
    expect(await save(w)).toBe(false)
    expect(api.updateGroupPolicy).toHaveBeenCalledWith(53, {
      expected_version: 63,
      policy: { enabled: true, ttft_seconds: 12.5, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 900 }
    })

    api.getGroupPolicy.mockResolvedValueOnce({ ...policy(), version: 64, policy: { ...policy().policy, ttft_seconds: 11 } })
    await w.get('[data-sq-policy-reload]').trigger('click')
    await flushPromises()
    expect((ttft(w).element as HTMLInputElement).value).toBe('11')
    expect(w.find('[data-sq-policy-reload]').exists()).toBe(false)

    api.updateGroupPolicy.mockResolvedValueOnce({ ...policy(), version: 65 })
    await error(w).setValue('7.5')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, expect.objectContaining({
      expected_version: 64,
      policy: expect.objectContaining({ ttft_seconds: 11, max_error_rate: 0.075 })
    }))
  })
})
