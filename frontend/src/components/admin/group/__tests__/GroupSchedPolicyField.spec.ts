import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { SchedBoard, SchedGroupPolicy } from '@/types/schedQuality'
import boardFixture from '@/__tests__/fixtures/schedQuality/board.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import GroupSchedPolicyField from '../GroupSchedPolicyField.vue'

const api = vi.hoisted(() => ({ getGroupPolicy: vi.fn(), updateGroupPolicy: vi.fn(), groupBoard: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const policy = () => structuredClone(policyFixture) as SchedGroupPolicy
// The contract board account (TTFT p90 9.2 s, fail bound 8.1 s; error 0.5 %; cache 93 %) plus one without enough data.
function board(): SchedBoard {
  const b = structuredClone(boardFixture) as SchedBoard
  const thin = structuredClone(b.accounts[0])
  Object.assign(thin, { account_id: 2, name: 'acc-thin', ttfo: { ...thin.ttfo, n: 12 }, error: { ...thin.error, n: 12 }, cache: { ...thin.cache, n: 3 } })
  b.accounts.push(thin)
  return b
}

const counts = (w: ReturnType<typeof mount>) =>
  ['pass', 'fail', 'nodata'].map((kind) => w.get(`[data-sq-trial="${kind}"]`).text())

async function mountField() {
  const w = mount(GroupSchedPolicyField, { props: { groupId: 53 }, global: { stubs: { Select: true } } })
  await flushPromises()
  return w
}

beforeEach(() => {
  api.getGroupPolicy.mockReset().mockResolvedValue(policy())
  api.updateGroupPolicy.mockReset()
  api.groupBoard.mockReset().mockResolvedValue(board())
})

describe('GroupSchedPolicyField', () => {
  it('shows the stored thresholds and the light / heavy / x-heavy limits of the selected model', async () => {
    const w = await mountField()
    expect((w.get('input[type="number"][step="0.1"][min="0.5"]').element as HTMLInputElement).value).toBe('10')
    expect(w.findAll('[data-sq-class-limits] > span').map((item) => item.text())).toEqual([
      'admin.schedQuality.class.light 7.3s', 'admin.schedQuality.class.heavy 12.0s', 'admin.schedQuality.class.xheavy 14.3s'
    ])
    expect(api.groupBoard).toHaveBeenCalledWith(53, 'gpt-6.1-sol')
  })

  it('marks out-of-range values without text, keeps the trial on the last valid value and blocks saving', async () => {
    const w = await mountField()
    const ttft = w.get('input[min="0.5"]')
    await ttft.setValue('0.4')
    expect(ttft.attributes('aria-invalid')).toBe('true')
    expect(ttft.classes()).toContain('input-error')
    await w.get('input[max="100"][step="1"]').setValue('101')
    expect(w.get('input[max="100"][step="1"]').attributes('aria-invalid')).toBe('true')
    expect((w.vm as unknown as { valid: boolean }).valid).toBe(false)
    expect(await (w.vm as unknown as { save: () => Promise<boolean> }).save()).toBe(false)
    expect(api.updateGroupPolicy).not.toHaveBeenCalled()
    expect(w.text()).not.toMatch(/[。.]$/)
  })

  it('recomputes the trial from the board numbers on every change without any request', async () => {
    const w = await mountField()
    expect(counts(w)).toEqual(['1', '0', '1'])
    const calls = api.groupBoard.mock.calls.length + api.getGroupPolicy.mock.calls.length

    await w.get('input[min="0.5"]').setValue('8')
    expect(counts(w)).toEqual(['0', '1', '1'])
    await w.get('input[min="0.5"]').setValue('10')
    await w.get('input[max="100"][step="1"]').setValue('95')
    expect(counts(w)).toEqual(['0', '1', '1'])
    expect(api.groupBoard.mock.calls.length + api.getGroupPolicy.mock.calls.length).toBe(calls)

    await w.get('[data-sq-trial="fail"]').trigger('click')
    expect(w.get('[data-sq-trial="fail"]').attributes('aria-expanded')).toBe('true')
    expect(w.get('[data-sq-trial-names]').text()).toBe('…')
  })

  it('saves with the version, and on a conflict shows failed + reload, which reads the server values back', async () => {
    const w = await mountField()
    await w.get('input[min="0.5"]').setValue('12.5')
    api.updateGroupPolicy.mockRejectedValueOnce({ status: 409, message: 'conflict' })
    expect(await (w.vm as unknown as { save: () => Promise<boolean> }).save()).toBe(false)
    expect(api.updateGroupPolicy).toHaveBeenCalledWith(53, {
      expected_version: 63,
      policy: { enabled: true, ttft_seconds: 12.5, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 0 }
    })
    expect(w.text()).toContain('admin.schedQuality.result.failed')

    api.getGroupPolicy.mockResolvedValueOnce({ ...policy(), version: 64, policy: { ...policy().policy, ttft_seconds: 11 } })
    await w.get('[data-sq-policy-reload]').trigger('click')
    await flushPromises()
    expect((w.get('input[min="0.5"]').element as HTMLInputElement).value).toBe('11')
    expect(w.find('[data-sq-policy-reload]').exists()).toBe(false)

    api.updateGroupPolicy.mockResolvedValueOnce({ ...policy(), version: 65 })
    await w.get('input[max="100"][step="0.1"]').setValue('2.5')
    expect(await (w.vm as unknown as { save: () => Promise<boolean> }).save()).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, expect.objectContaining({
      expected_version: 64,
      policy: expect.objectContaining({ ttft_seconds: 11, max_error_rate: 0.025 })
    }))
  })
})
