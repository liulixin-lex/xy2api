import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { BackendGroupPolicy, BackendPolicyPreview } from '@/api/admin/schedQualityTypes'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import GroupSchedPolicyField from '../GroupSchedPolicyField.vue'

const api = vi.hoisted(() => ({ getGroupPolicy: vi.fn(), updateGroupPolicy: vi.fn(), previewGroupPolicy: vi.fn(), groupBoard: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const policy = () => structuredClone(policyFixture) as BackendGroupPolicy
const preview = (over: Partial<BackendPolicyPreview> = {}): BackendPolicyPreview => ({
  now: { normal: 3, below: 1 },
  after: { normal: 2, below: 2 },
  changed: [{ account_id: 7, name: 'acc-07', model: 'gpt-6.1-sol', dims: ['ttfo'] }],
  ...over
})

const ttft = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-ttft-"]')
const error = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-err-"]')
const cache = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-cache-"]')
const save = (w: ReturnType<typeof mount>) => (w.vm as unknown as { save: () => Promise<boolean> }).save()
const trial = (w: ReturnType<typeof mount>) => w.find('[data-sq-trial]')
const counts = (w: ReturnType<typeof mount>) => ['normal', 'cooling'].map((kind) => w.get(`[data-sq-trial="${kind}"]`).text())

async function mountField() {
  const w = mount(GroupSchedPolicyField, { props: { groupId: 53 }, global: { stubs: { Select: true } } })
  await flushPromises()
  return w
}
/** Past the typing pause, then the preview answer. */
async function settle() {
  await vi.advanceTimersByTimeAsync(400)
  await flushPromises()
}

beforeEach(() => {
  api.getGroupPolicy.mockReset().mockResolvedValue(policy())
  api.updateGroupPolicy.mockReset()
  api.previewGroupPolicy.mockReset().mockResolvedValue(preview())
  api.groupBoard.mockReset()
})
afterEach(() => { vi.useRealTimers() })

describe('GroupSchedPolicyField', () => {
  it('shows the stored thresholds without per-class limits or a model pick, and loads no board (backend 0.6.5)', async () => {
    const w = await mountField()
    expect((ttft(w).element as HTMLInputElement).value).toBe('10')
    expect((error(w).element as HTMLInputElement).value).toBe('5')
    expect((cache(w).element as HTMLInputElement).value).toBe('90')
    expect(w.find('[data-sq-class-limits]').exists()).toBe(false)
    expect(trial(w).exists()).toBe(false)
    expect(api.groupBoard).not.toHaveBeenCalled()
    expect(api.previewGroupPolicy).not.toHaveBeenCalled()
  })

  it('keeps the API precision of untouched thresholds: 0.875 round-trips and an unchanged block sends no PUT', async () => {
    const p = policy()
    Object.assign(p.policy, { min_cache_ratio: 0.875, max_error_rate: 0.06234 })
    api.getGroupPolicy.mockResolvedValue(p)
    const w = await mountField()
    expect((cache(w).element as HTMLInputElement).value).toBe('88')
    expect((error(w).element as HTMLInputElement).value).toBe('6.23')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).not.toHaveBeenCalled()

    // Another field changed: the untouched thresholds go back exactly as they came.
    api.updateGroupPolicy.mockResolvedValueOnce({ ...p, version: 64, policy: { ...p.policy, enabled: false } })
    await w.get('[role="switch"]').trigger('click')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, {
      expected_version: 63,
      policy: { enabled: false, ttft_seconds: 10, max_error_rate: 0.06234, min_cache_ratio: 0.875, takeover_idle_seconds: 0 }
    })

    // A field the user typed in is taken as typed.
    await cache(w).setValue('88')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, {
      expected_version: 64,
      policy: { enabled: false, ttft_seconds: 10, max_error_rate: 0.06234, min_cache_ratio: 0.88, takeover_idle_seconds: 0 }
    })
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

  it('marks values the backend rejects without text (first output 0, errors under 5 %, cache over 100 %), asks no preview and blocks saving', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const w = await mountField()
    await ttft(w).setValue('0')
    expect(ttft(w).attributes('aria-invalid')).toBe('true')
    expect(ttft(w).classes()).toContain('input-error')
    await ttft(w).setValue('600.5')
    expect(ttft(w).attributes('aria-invalid')).toBe('false')
    await error(w).setValue('4.9')
    expect(error(w).attributes('aria-invalid')).toBe('true')
    expect(error(w).attributes('min')).toBe('5')
    await cache(w).setValue('101')
    expect(cache(w).attributes('aria-invalid')).toBe('true')
    expect((w.vm as unknown as { valid: boolean }).valid).toBe(false)
    expect(await save(w)).toBe(false)
    await settle()
    expect(trial(w).exists()).toBe(false)
    expect(api.previewGroupPolicy).not.toHaveBeenCalled()
    expect(api.updateGroupPolicy).not.toHaveBeenCalled()
    expect(w.text()).not.toMatch(/[。.]$/)
  })

  it('asks the server preview after a typing pause and shows the counts now → after and the accounts that turn', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const w = await mountField()
    await ttft(w).setValue('9')
    await ttft(w).setValue('8')
    // Still typing: nothing asked yet, the row waits with a skeleton.
    expect(api.previewGroupPolicy).not.toHaveBeenCalled()
    expect(trial(w).find('.skeleton').exists()).toBe(true)
    await settle()
    // Only the last value is asked for, with the saved version.
    expect(api.previewGroupPolicy).toHaveBeenCalledTimes(1)
    expect(api.previewGroupPolicy).toHaveBeenCalledWith(53, {
      expected_version: 63,
      policy: { enabled: true, ttft_seconds: 8, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 0 }
    }, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(counts(w)).toEqual([
      'admin.schedQuality.policy.trialCount:{"state":"admin.schedQuality.state.normal","now":3,"after":2}',
      'admin.schedQuality.policy.trialCount:{"state":"admin.schedQuality.state.cooling","now":1,"after":2}'
    ])
    expect(w.get('[data-sq-trial-names]').text()).toBe('acc-07')

    // Back to the saved value: the row goes and nothing more is asked.
    await ttft(w).setValue('10')
    await settle()
    expect(trial(w).exists()).toBe(false)
    expect(api.previewGroupPolicy).toHaveBeenCalledTimes(1)
  })

  it('drops a preview answer that a later edit replaced, shows 失败 for a failed one, and lists at most 12 names', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    let release: (value: BackendPolicyPreview) => void = () => {}
    api.previewGroupPolicy.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
    const w = await mountField()
    await cache(w).setValue('95')
    await settle()
    const first = api.previewGroupPolicy.mock.calls[0][2].signal as AbortSignal

    const many = Array.from({ length: 14 }, (_, i) => ({ account_id: i + 1, name: `acc-${i + 1}`, model: 'gpt-6.1-sol', dims: ['cache' as const] }))
    api.previewGroupPolicy.mockResolvedValueOnce(preview({ changed: many }))
    await cache(w).setValue('96')
    expect(first.aborted).toBe(true)
    await settle()
    release(preview({ after: { normal: 0, below: 4 } }))
    await flushPromises()
    expect(counts(w)[0]).toContain('"after":2')
    const names = w.findAll('[data-sq-trial-names] > span').map((el) => el.text())
    expect(names).toHaveLength(13)
    expect(names.slice(-1)).toEqual(['admin.schedQuality.more:{"n":2}'])

    api.previewGroupPolicy.mockRejectedValueOnce({ status: 500 })
    await cache(w).setValue('97')
    await settle()
    expect(w.get('[data-sq-trial-failed]').text()).toBe('admin.schedQuality.result.failed')
  })

  it('saves with the version, and on a conflict shows failed + reload, which reads the server values back', async () => {
    const w = await mountField()
    await ttft(w).setValue('12.5')
    api.updateGroupPolicy.mockRejectedValueOnce({ status: 409, message: 'conflict' })
    expect(await save(w)).toBe(false)
    expect(api.updateGroupPolicy).toHaveBeenCalledWith(53, {
      expected_version: 63,
      policy: { enabled: true, ttft_seconds: 12.5, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 0 }
    })
    expect(w.text()).toContain('admin.schedQuality.result.failed')

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
