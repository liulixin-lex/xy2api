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
  reach: [
    { model: 'gpt-6.1-sol', cache: { ok: 5, n: 8 }, first_output: { ok: 3, n: 8 } },
    { model: 'gpt-6.2-astra', cache: { ok: 0, n: 0 }, first_output: { ok: 4, n: 8 } }
  ],
  ...over
})

const ttft = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-ttft-"]')
const error = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-err-"]')
const cache = (w: ReturnType<typeof mount>) => w.get('input[id^="sq-cache-"]')
const save = (w: ReturnType<typeof mount>) => (w.vm as unknown as { save: () => Promise<boolean> }).save()
/** Each model's count under the input of a line, with its colour: [model, "ok / n", amber | plain | grey]. */
const reachOf = (w: ReturnType<typeof mount>, line: 'ttfo' | 'cache') =>
  w.findAll(`[data-sq-reach="${line}"] [data-sq-reach-model]`).map((item) => {
    const [name, count] = item.findAll('span')
    const tone = count.classes().includes('text-amber-700') ? 'amber' : count.classes().includes('text-gray-400') ? 'grey' : 'plain'
    return [name.text(), count.text(), tone]
  })

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
    expect(api.groupBoard).not.toHaveBeenCalled()
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

    // The lowest error limit, 1 %, is saved as the ratio 0.01.
    api.updateGroupPolicy.mockResolvedValueOnce({ ...p, version: 65 })
    await error(w).setValue('1')
    expect(await save(w)).toBe(true)
    expect(api.updateGroupPolicy).toHaveBeenLastCalledWith(53, {
      expected_version: 64,
      policy: { enabled: false, ttft_seconds: 10, max_error_rate: 0.01, min_cache_ratio: 0.88, takeover_idle_seconds: 0 }
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

  it('marks values the backend rejects without text (first output 0, errors under 1 %, cache over 100 %), asks no preview and blocks saving', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const w = await mountField()
    await ttft(w).setValue('0')
    expect(ttft(w).attributes('aria-invalid')).toBe('true')
    expect(ttft(w).classes()).toContain('input-error')
    await ttft(w).setValue('600.5')
    expect(ttft(w).attributes('aria-invalid')).toBe('false')
    // The floor is 1 %: 0.9 is refused, 1 and the old floor's neighbour 4.9 are not.
    for (const [value, invalid] of [['1', 'false'], ['4.9', 'false'], ['0.9', 'true']]) {
      await error(w).setValue(value)
      expect(error(w).attributes('aria-invalid')).toBe(invalid)
    }
    expect(error(w).attributes('min')).toBe('1')
    await cache(w).setValue('101')
    expect(cache(w).attributes('aria-invalid')).toBe('true')
    expect((w.vm as unknown as { valid: boolean }).valid).toBe(false)
    expect(await save(w)).toBe(false)
    await settle()
    expect(api.previewGroupPolicy).not.toHaveBeenCalled()
    expect(api.updateGroupPolicy).not.toHaveBeenCalled()
    expect(w.text()).not.toMatch(/[。.]$/)
  })

  // Plan item 19: before any change, the saved thresholds' reach shows under the first-output limit and the cache
  // minimum, each model with "ok / n"; then it follows the values typed, asked once after the typing pause.
  it('shows each model\'s reach under its input on opening, then follows the typed values after a pause', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const w = await mountField()
    // Before the first answer: a skeleton where the counts go, nothing asked yet.
    expect(w.get('[data-sq-reach="ttfo"]').find('.skeleton').exists()).toBe(true)
    expect(api.previewGroupPolicy).not.toHaveBeenCalled()
    await settle()
    expect(api.previewGroupPolicy).toHaveBeenCalledTimes(1)
    expect(api.previewGroupPolicy).toHaveBeenLastCalledWith(53, {
      expected_version: 63,
      policy: { enabled: true, ttft_seconds: 10, max_error_rate: 0.05, min_cache_ratio: 0.9, takeover_idle_seconds: 0 }
    }, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    // Fewer than half amber, half plain; a model without any account with enough samples is a grey dash, not 0 / 0.
    expect(reachOf(w, 'ttfo')).toEqual([['gpt-6.1-sol', '3 / 8', 'amber'], ['gpt-6.2-astra', '4 / 8', 'plain']])
    expect(reachOf(w, 'cache')).toEqual([['gpt-6.1-sol', '5 / 8', 'plain'], ['gpt-6.2-astra', '—', 'grey']])
    // No labels, no sentence: the row sits under its own input. The previous preview is gone.
    expect(w.find('[data-sq-reach="error"]').exists()).toBe(false)
    expect(w.find('[data-sq-trial], [data-sq-trial-note], [data-sq-trial-names]').exists()).toBe(false)
    expect(w.find('[data-sq-relaxed]').exists()).toBe(false)

    // The cache minimum lowered from 90 to 80: asked once for the last value, and the counts change with it.
    api.previewGroupPolicy.mockResolvedValueOnce(preview({ reach: [{ model: 'gpt-6.1-sol', cache: { ok: 7, n: 8 }, first_output: { ok: 3, n: 8 } }] }))
    await cache(w).setValue('85')
    await cache(w).setValue('80')
    await settle()
    expect(api.previewGroupPolicy).toHaveBeenCalledTimes(2)
    expect(api.previewGroupPolicy.mock.calls[1][1].policy.min_cache_ratio).toBe(0.8)
    expect(reachOf(w, 'cache')).toEqual([['gpt-6.1-sol', '7 / 8', 'plain']])

    // An answer without counts (the 12-hour figures still loading): a dash, the row kept.
    api.previewGroupPolicy.mockResolvedValueOnce(preview({ reach: [] }))
    await ttft(w).setValue('8')
    await settle()
    expect(reachOf(w, 'ttfo')).toEqual([])
    expect(w.get('[data-sq-reach="ttfo"]').text()).toBe('—')
  })

  it('drops an answer a later edit replaced, dims the last counts while invalid, and shows 失败 for a failed one', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const w = await mountField()
    await settle()
    let release: (value: BackendPolicyPreview) => void = () => {}
    api.previewGroupPolicy.mockImplementationOnce(() => new Promise((resolve) => { release = resolve }))
    await ttft(w).setValue('12.5')
    await settle()
    const slow = api.previewGroupPolicy.mock.calls[1][2].signal as AbortSignal
    // Waiting for it, the last counts stay, dimmed.
    expect(w.get('[data-sq-reach="ttfo"]').classes()).toContain('opacity-60')

    api.previewGroupPolicy.mockResolvedValueOnce(preview({ reach: [{ model: 'gpt-6.1-sol', cache: { ok: 5, n: 8 }, first_output: { ok: 6, n: 8 } }] }))
    await ttft(w).setValue('13')
    expect(slow.aborted).toBe(true)
    await settle()
    expect(reachOf(w, 'ttfo')).toEqual([['gpt-6.1-sol', '6 / 8', 'plain']])
    // The replaced request answers late: its counts do not cover the newer ones.
    release(preview({ reach: [{ model: 'gpt-6.1-sol', cache: { ok: 1, n: 8 }, first_output: { ok: 1, n: 8 } }] }))
    await flushPromises()
    expect(reachOf(w, 'ttfo')).toEqual([['gpt-6.1-sol', '6 / 8', 'plain']])
    expect(w.get('[data-sq-reach="ttfo"]').classes()).not.toContain('opacity-60')

    // An invalid value asks nothing; the last counts stay, dimmed.
    await ttft(w).setValue('0')
    await settle()
    expect(api.previewGroupPolicy).toHaveBeenCalledTimes(3)
    expect(reachOf(w, 'ttfo')).toEqual([['gpt-6.1-sol', '6 / 8', 'plain']])
    expect(w.get('[data-sq-reach="ttfo"]').classes()).toContain('opacity-60')

    api.previewGroupPolicy.mockRejectedValueOnce({ status: 500 })
    await ttft(w).setValue('14')
    await settle()
    expect(w.get('[data-sq-reach="ttfo"] [data-sq-reach-failed]').text()).toBe('admin.schedQuality.result.failed')
    expect(w.get('[data-sq-reach="cache"] [data-sq-reach-failed]').text()).toBe('admin.schedQuality.result.failed')
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

  // What saving does stays beside the field whose new value relaxes its line (a consequence, not a note).
  it('marks beside its field each line saving relaxes, only while the form differs, and tells the result of a relaxing save', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    // An answer for unchanged values never marks a line, whatever it says.
    api.previewGroupPolicy.mockResolvedValueOnce(preview({ relaxed_lines: ['cache'] }))
    const w = await mountField()
    await settle()
    expect(w.find('[data-sq-relaxed]').exists()).toBe(false)
    api.previewGroupPolicy.mockResolvedValueOnce(preview({ relaxed_lines: ['ttfo', 'error'] }))
    await ttft(w).setValue('12')
    await settle()
    expect(w.findAll('[data-sq-relaxed]').map((el) => [el.attributes('data-sq-relaxed'), el.text()])).toEqual([
      ['ttfo', 'admin.schedQuality.policy.trialRelaxed'], ['error', 'admin.schedQuality.policy.trialRelaxed']
    ])

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

  // Review item 8: by the saved floor, like the error examples; it follows a successful save, not the typing.
  // With a high limit the backend lists only the examples that can trip: one, or none (not judged bad within 100).
  it.each([
    ['one example', { effective_max_error_rate: 0.3, error_trip_examples: [{ known: 100, failed: 39 }] },
      `admin.schedQuality.policy.errorTrip:${JSON.stringify({ limit: '30%', examples: 'admin.schedQuality.policy.errorTripItem:{"known":100,"failed":39}' })}`],
    ['no example', { effective_max_error_rate: 0.9, error_trip_examples: [] }, 'admin.schedQuality.policy.errorTripNone:{"limit":"90%"}'],
    ['an older answer without the fields', { effective_max_error_rate: undefined, error_trip_examples: undefined }, null],
    ['an older answer without the examples', { error_trip_examples: undefined }, null]
  ])('explains the saved error limit with %s', async (_, patch, expected) => {
    api.getGroupPolicy.mockResolvedValue({ ...policy(), ...patch })
    const w = await mountField()
    const note = w.find('[data-sq-policy-notes="error"]')
    if (expected === null) expect(note.exists()).toBe(false)
    else expect(note.text()).toBe(expected)
  })

  it('warns about a saved cache floor above 85 %, unchanged by typing until a save, and always says a passed trial is not stability', async () => {
    const w = await mountField()
    const warned = () => w.find('[data-sq-policy-notes="cache"]').exists()
    // The fixture's saved floor is 90 %.
    expect(w.get('[data-sq-policy-notes="cache"]').text()).toBe('admin.schedQuality.policy.cacheHigh')
    await cache(w).setValue('85')
    expect(warned()).toBe(true)
    api.updateGroupPolicy.mockResolvedValue({ ...policy(), version: 64, policy: { ...policy().policy, min_cache_ratio: 0.85 }, relaxed_lines: [], cleared: 0 })
    expect(await save(w)).toBe(true)
    await flushPromises()
    expect(warned()).toBe(false)
    await cache(w).setValue('95')
    expect(warned()).toBe(false)
    expect(w.get('[data-sq-policy-notes="trial"]').text()).toBe('admin.schedQuality.policy.trialStable')
  })

  it.each([[0.85, false], [0.86, true]])('warns at a saved floor of %s: %s', async (floor, expected) => {
    api.getGroupPolicy.mockResolvedValue({ ...policy(), policy: { ...policy().policy, min_cache_ratio: floor } })
    const w = await mountField()
    expect(w.find('[data-sq-policy-notes="cache"]').exists()).toBe(expected)
  })
})
