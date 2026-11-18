import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { BackendGroupPolicy, BackendPolicyPreview } from '@/api/admin/schedQualityTypes'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import GroupSchedPolicyField from '../GroupSchedPolicyField.vue'

const api = vi.hoisted(() => ({ getGroupPolicy: vi.fn(), updateGroupPolicy: vi.fn(), previewGroupPolicy: vi.fn(), groupBoard: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const toast = vi.hoisted(() => ({ showInfo: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key), te: () => true })
}))

const policy = () => structuredClone(policyFixture) as BackendGroupPolicy
const preview = (over: Partial<BackendPolicyPreview> = {}): BackendPolicyPreview => ({
  relaxed_lines: [],
  lines: {
    ttfo: { now: { pass: 3, unknown: 1, fail: 0 }, after: { pass: 2, unknown: 1, fail: 1 } },
    error: { now: { pass: 4, unknown: 0, fail: 0 }, after: { pass: 4, unknown: 0, fail: 0 } },
    cache: { now: { pass: 2, unknown: 2, fail: 0 }, after: { pass: 2, unknown: 2, fail: 0 } }
  },
  changed: [{ account_id: 7, name: 'acc-07', model: 'gpt-6.1-sol', line: 'ttfo', from: 'pass', to: 'fail' }],
  ...over
})

const ttft = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-ttft-"]')
const error = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-err-"]')
const cache = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-cache-"]')
const save = (w: ReturnType<typeof mount>) => (w.vm as unknown as { save: () => Promise<boolean> }).save()
const trial = (w: ReturnType<typeof mount>) => w.find('[data-sq-trial]')
const counts = (w: ReturnType<typeof mount>) => ['ttfo', 'error', 'cache'].map((line) => w.get(`[data-sq-trial="${line}"]`).text())

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
  toast.showInfo.mockReset()
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

  it('asks the server preview after a typing pause and shows each line\'s verdicts now → after and the keys that turn', async () => {
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
      'admin.schedQuality.dim.ttfoadmin.schedQuality.policy.trialLine:{"pass":"3→2","unknown":"1→1","fail":"0→1"}',
      'admin.schedQuality.dim.erroradmin.schedQuality.policy.trialLine:{"pass":"4→4","unknown":"0→0","fail":"0→0"}',
      'admin.schedQuality.dim.cacheadmin.schedQuality.policy.trialLine:{"pass":"2→2","unknown":"2→2","fail":"0→0"}'
    ])
    expect(w.get('[data-sq-trial-names]').text()).toBe('acc-07')
    // What saving does: relaxing clears and re-judges, tightening keeps identities.
    expect(w.get('[data-sq-trial-note]').text()).toBe('admin.schedQuality.policy.trialNote')
    expect(w.find('[data-sq-trial-relaxed]').exists()).toBe(false)

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

    const many = Array.from({ length: 14 }, (_, i) => ({ account_id: i + 1, name: `acc-${i + 1}`, model: 'gpt-6.1-sol', line: 'cache' as const, from: 'pass' as const, to: 'fail' as const }))
    api.previewGroupPolicy.mockResolvedValueOnce(preview({ changed: many }))
    await cache(w).setValue('96')
    expect(first.aborted).toBe(true)
    await settle()
    release(preview({ lines: { ...preview().lines, ttfo: { now: { pass: 9, unknown: 9, fail: 9 }, after: { pass: 9, unknown: 9, fail: 9 } } } }))
    await flushPromises()
    expect(counts(w)[0]).toContain('"pass":"3→2"')
    const names = w.findAll('[data-sq-trial-names] > span').map((el) => el.text())
    expect(names).toHaveLength(13)
    expect(names.slice(-1)).toEqual(['admin.schedQuality.more:{"n":2}'])

    api.previewGroupPolicy.mockRejectedValueOnce({ status: 500 })
    await cache(w).setValue('97')
    await settle()
    expect(w.get('[data-sq-trial-failed]').text()).toBe('admin.schedQuality.result.failed')
  })

  it('lists an account with several models changing once, naming the models, with every change in the tip', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    api.previewGroupPolicy.mockResolvedValueOnce(preview({
      changed: [
        { account_id: 1, name: 'acc-01', model: 'gpt-a', line: 'ttfo', from: 'pass', to: 'fail' },
        { account_id: 2, name: 'acc-02', model: 'gpt-a', line: 'ttfo', from: 'pass', to: 'fail' },
        { account_id: 1, name: 'acc-01', model: 'gpt-b', line: 'ttfo', from: 'unknown', to: 'fail' },
        { account_id: 1, name: 'acc-01', model: 'gpt-a', line: 'error', from: 'pass', to: 'unknown' }
      ]
    }))
    const w = await mountField()
    await ttft(w).setValue('8')
    await settle()
    const spans = w.findAll('[data-sq-trial-names] > span')
    expect(spans.map((el) => el.text())).toEqual(['acc-01 · gpt-aadmin.schedQuality.evidence.joinergpt-b', 'acc-02'])
    expect((spans[0].element as HTMLElement & { __tipText?: string }).__tipText).toBe([
      'gpt-a · admin.schedQuality.dim.ttfo · admin.schedQuality.verdict.pass → admin.schedQuality.verdict.fail',
      'gpt-b · admin.schedQuality.dim.ttfo · admin.schedQuality.verdict.unknown → admin.schedQuality.verdict.fail',
      'gpt-a · admin.schedQuality.dim.error · admin.schedQuality.verdict.pass → admin.schedQuality.verdict.unknown'
    ].join('\n'))
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

  it('marks the lines saving relaxes, a line missing from the answer as "—", and tells the result of a relaxing save', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const { cache: _missing, ...lines } = preview().lines
    api.previewGroupPolicy.mockResolvedValueOnce({ ...preview({ relaxed_lines: ['ttfo'] }), lines })
    const w = await mountField()
    await ttft(w).setValue('12')
    await settle()
    expect(w.get('[data-sq-trial="ttfo"]').find('[data-sq-trial-relaxed]').exists()).toBe(true)
    expect(w.get('[data-sq-trial="error"]').find('[data-sq-trial-relaxed]').exists()).toBe(false)
    expect(w.get('[data-sq-trial="cache"]').text()).toContain('"pass":"—"')

    api.updateGroupPolicy.mockResolvedValueOnce({ ...policy(), version: 64, relaxed_lines: ['ttfo'], cleared: 7 })
    expect(await save(w)).toBe(true)
    expect(toast.showInfo).toHaveBeenCalledWith('admin.schedQuality.policy.savedRelaxed:{"lines":"admin.schedQuality.lineName.ttfo","n":7}', 6000)

    // An answer without relaxed_lines and cleared (a tightening, or a backend that leaves them out) saves quietly.
    api.updateGroupPolicy.mockResolvedValueOnce({ group_id: 53, version: 65, policy: { ...policy().policy, ttft_seconds: 9 } })
    await ttft(w).setValue('9')
    expect(await save(w)).toBe(true)
    expect(toast.showInfo).toHaveBeenCalledTimes(1)
  })

  // Round 3 (plan item 9): what the saved error limit takes to judge a line bad, by the backend's numbers.
  it('explains the saved error limit with the backend\'s examples, not the typed value, until a save answers with new ones', async () => {
    const w = await mountField()
    const tripText = (limit: string, examples: { known: number; failed: number }[]) => `admin.schedQuality.policy.errorTrip:${JSON.stringify({
      limit,
      examples: examples.map((item) => `admin.schedQuality.policy.errorTripItem:${JSON.stringify(item)}`).join('admin.schedQuality.policy.errorTripJoiner')
    })}`
    const note = () => w.get('[data-sq-policy-notes="error"]').text()
    expect(note()).toBe(tripText('5%', [{ known: 50, failed: 6 }, { known: 100, failed: 10 }]))
    await error(w).setValue('8')
    expect(note()).toBe(tripText('5%', [{ known: 50, failed: 6 }, { known: 100, failed: 10 }]))
    const saved = { ...policy(), version: 64, policy: { ...policy().policy, max_error_rate: 0.08 }, effective_max_error_rate: 0.08,
      error_trip_examples: [{ known: 50, failed: 8 }, { known: 100, failed: 13 }], relaxed_lines: [], cleared: 0 }
    api.updateGroupPolicy.mockResolvedValue(saved)
    expect(await save(w)).toBe(true)
    await flushPromises()
    expect(note()).toBe(tripText('8%', [{ known: 50, failed: 8 }, { known: 100, failed: 13 }]))
  })

  it('warns about a cache floor above 85 % as typed, and always says a passed trial is not stability', async () => {
    const w = await mountField()
    // The fixture's floor is 90 %.
    expect(w.find('[data-sq-policy-notes="cache"]').text()).toBe('admin.schedQuality.policy.cacheHigh')
    await cache(w).setValue('85')
    expect(w.find('[data-sq-policy-notes="cache"]').exists()).toBe(false)
    await cache(w).setValue('86')
    expect(w.find('[data-sq-policy-notes="cache"]').exists()).toBe(true)
    expect(w.get('[data-sq-policy-notes="trial"]').text()).toBe('admin.schedQuality.policy.trialStable')
  })
})
