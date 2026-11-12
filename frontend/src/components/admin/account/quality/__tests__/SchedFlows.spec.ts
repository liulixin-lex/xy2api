import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach } from 'vitest'

import resetFixture from '@/__tests__/fixtures/schedQuality/reset.json'
import SchedResetDialog from '../SchedResetDialog.vue'
import SchedBatchDialog from '../SchedBatchDialog.vue'
import { closeSchedReset, openSchedReset } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ reset: vi.fn(), startTest: vi.fn(), setOverride: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => !key.endsWith('.brand_new')
  })
}))

enableAutoUnmount(afterEach)
beforeEach(() => {
  // Leave transitions of dialogs from the previous test never finish in jsdom.
  document.body.innerHTML = ''
  closeSchedReset()
  Object.values(api).forEach((fn) => fn.mockReset())
  document.documentElement.dataset.motion = 'reduced'
})
afterAll(() => { delete document.documentElement.dataset.motion })

const click = async (selector: string) => {
  document.querySelector<HTMLElement>(selector)!.click()
  await flushPromises()
}

describe('reset flow', () => {
  it('confirms with the account name, then lists cleared, kept and failed items as short names in the same dialog', async () => {
    api.reset.mockResolvedValue({ ...resetFixture, failed: [...resetFixture.failed, { key: 'x', kind: 'brand_new', error: 'boom' }] })
    const wrapper = mount(SchedResetDialog, { attachTo: document.body })
    openSchedReset({ id: 479, name: 'acc-03' })
    await flushPromises()
    expect(document.body.textContent).toContain('acc-03')
    expect(document.querySelector('[data-sq-reset-result]')).toBeNull()

    await click('[data-sq-reset-confirm]')
    expect(api.reset).toHaveBeenCalledWith(479)
    const column = (key: string) => Array.from(document.querySelectorAll(`[data-column="${key}"] li`)).map((li) => li.textContent?.replace(/\s+/g, ' ').trim())
    expect(column('cleared')).toEqual(['admin.schedQuality.kind.quality', 'admin.schedQuality.kind.model_transient gpt-5.6-sol'])
    expect(column('preserved')).toEqual(['admin.schedQuality.kind.iq_gate gpt-6-astra'])
    expect(column('failed')).toEqual(['admin.schedQuality.kind.rate_limit', 'admin.schedQuality.unknown'])
    // Neither the reset time nor reasons or errors are shown.
    expect(document.body.textContent).not.toContain('boom')

    document.querySelector<HTMLElement>('.modal-footer button')!.click()
    await flushPromises()
    expect(wrapper.emitted('done')?.[0]).toEqual([479])
  })
})

describe('batch flow', () => {
  const accounts = [{ id: 1, name: 'acc-a' }, { id: 2, name: 'acc-b' }, { id: 3, name: 'acc-c' }]
  const results = () => Array.from(document.querySelectorAll('[data-sq-batch-row]')).map((tr) => [
    tr.querySelector('td')!.textContent, tr.querySelector('td:last-child')!.textContent?.trim()
  ])

  it('confirms a batch test with the account count, then lists each account with its result', async () => {
    api.startTest.mockImplementation(async (id: number) => ({
      tests: id === 1 ? [{ model: 'm', test_id: 't', started: true, why: '' }]
        : id === 2 ? [{ model: 'm', test_id: '', started: false, why: 'already_running' }]
          : [{ model: 'm', test_id: '', started: false, why: 'too_soon' }]
    }))
    const wrapper = mount(SchedBatchDialog, { props: { action: 'test', accounts, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    expect(document.body.textContent).toContain('admin.schedQuality.col.account')
    expect(document.body.textContent).toContain('3')
    expect(api.startTest).not.toHaveBeenCalled()

    await click('[data-sq-batch-confirm]')
    expect(results()).toEqual([
      ['acc-a', 'admin.schedQuality.result.ok'],
      ['acc-b', 'admin.schedQuality.state.testing'],
      ['acc-c', 'admin.schedQuality.result.failed']
    ])
    expect(wrapper.emitted('done')).toHaveLength(1)
  })

  it('counts a test where a model hit an internal error as failed, with started tickets kept apart (K28)', async () => {
    api.startTest.mockImplementation(async (id: number) => ({
      tests: id === 1
        ? [{ model: 'a', test_id: 't1', started: true, why: '' }, { model: 'b', test_id: '', started: false, why: 'error' }]
        : id === 2
          ? [{ model: 'a', test_id: 't2', started: true, why: '' }, { model: 'b', test_id: '', started: false, why: 'too_soon' }]
          : [{ model: 'a', test_id: '', started: false, why: 'already_running' }, { model: 'b', test_id: '', started: false, why: 'error' }]
    }))
    mount(SchedBatchDialog, { props: { action: 'test', accounts, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    await click('[data-sq-batch-confirm]')
    expect(results()).toEqual([
      ['acc-a', 'admin.schedQuality.result.failed'],
      ['acc-b', 'admin.schedQuality.result.ok'],
      ['acc-c', 'admin.schedQuality.result.failed']
    ])
  })

  it('runs a batch reset four at a time and marks request failures', async () => {
    let running = 0
    let peak = 0
    const many = Array.from({ length: 9 }, (_, i) => ({ id: i + 1, name: `acc-${i + 1}` }))
    api.reset.mockImplementation(async (id: number) => {
      running++
      peak = Math.max(peak, running)
      await new Promise((resolve) => setTimeout(resolve))
      running--
      if (id === 5) throw new Error('down')
      return { ...resetFixture, failed: [] }
    })
    mount(SchedBatchDialog, { props: { action: 'reset', accounts: many, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    await click('[data-sq-batch-confirm]')
    await new Promise((resolve) => setTimeout(resolve, 20))
    await flushPromises()
    expect(peak).toBe(4)
    expect(results().filter(([, result]) => result === 'admin.schedQuality.result.failed').map(([name]) => name)).toEqual(['acc-5'])
    expect(results()).toHaveLength(9)
  })

  // One answer per account, released by the test.
  function deferred() {
    const pending = new Map<number, { resolve: (value: unknown) => void; reject: (error: unknown) => void }>()
    const call = (id: number) => new Promise((resolve, reject) => { pending.set(id, { resolve, reject }) })
    return { pending, call }
  }
  const totals = () => Array.from(document.querySelectorAll('.modal-footer p span.font-medium')).map((el) => el.textContent)

  it('counts a reset that answers 200 with failed items as failed, per account and in the totals', async () => {
    const answers: Record<number, () => Promise<unknown>> = {
      1: async () => ({ ...resetFixture, failed: [] }),
      2: async () => ({ ...resetFixture, failed: [{ key: 'rate_limit', kind: 'rate_limit', error: 'x' }] }),
      3: async () => { throw new Error('network') }
    }
    api.reset.mockImplementation((id: number) => answers[id]())
    mount(SchedBatchDialog, { props: { action: 'reset', accounts, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    await click('[data-sq-batch-confirm]')
    expect(results()).toEqual([
      ['acc-a', 'admin.schedQuality.result.ok'],
      ['acc-b', 'admin.schedQuality.result.failed'],
      ['acc-c', 'admin.schedQuality.result.failed']
    ])
    expect(totals()).toEqual(['1', '2'])
  })

  it('counts an all-clear batch reset as success', async () => {
    api.reset.mockResolvedValue({ ...resetFixture, failed: [] })
    mount(SchedBatchDialog, { props: { action: 'reset', accounts, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    await click('[data-sq-batch-confirm]')
    expect(results().map(([, result]) => result)).toEqual(Array(3).fill('admin.schedQuality.result.ok'))
    expect(totals()).toEqual(['3', '0'])
  })

  it('keeps the action, request and accounts it started with when the props change mid-run', async () => {
    const answers = deferred()
    api.setOverride.mockImplementation((id: number) => answers.call(id))
    const many = Array.from({ length: 6 }, (_, i) => ({ id: i + 1, name: `acc-${i + 1}` }))
    const wrapper = mount(SchedBatchDialog, { props: { action: 'demote', accounts: many, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    // The popover confirms with an empty reason: it is sent as is (contract clarification K22).
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    const request = api.setOverride.mock.calls[0][1]
    expect(request).toMatchObject({ group_id: 0, mode: 'demote', reason: '' })
    expect(api.setOverride).toHaveBeenCalledTimes(4)

    await wrapper.setProps({ action: 'exempt', accounts: [{ id: 99, name: 'other' }] })
    for (const id of [1, 2, 3, 4]) answers.pending.get(id)!.resolve(undefined)
    await flushPromises()
    for (const id of [5, 6]) answers.pending.get(id)!.resolve(undefined)
    await flushPromises()
    expect(api.setOverride.mock.calls.map(([id, body]) => [id, body])).toEqual(many.map(({ id }) => [id, request]))
    expect(results()).toEqual(many.map(({ name }) => [name, 'admin.schedQuality.result.ok']))
  })

  it('stops at close: later accounts are not run, late answers are dropped, and no batch starts until they are in', async () => {
    const answers = deferred()
    api.reset.mockImplementation((id: number) => answers.call(id))
    api.startTest.mockResolvedValue({ tests: [{ model: 'm', test_id: 't', started: true, why: '' }] })
    const many = Array.from({ length: 8 }, (_, i) => ({ id: i + 1, name: `acc-${i + 1}` }))
    const wrapper = mount(SchedBatchDialog, { props: { action: 'reset', accounts: many, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    await click('[data-sq-batch-confirm]')
    expect(wrapper.emitted('busy')).toEqual([[true]])
    answers.pending.get(1)!.resolve({ ...resetFixture, failed: [] })
    await flushPromises()
    expect(api.reset).toHaveBeenCalledTimes(5)

    // Close the results while 2, 3, 4 and 5 are on their way.
    await click('[data-sq-batch-results] ~ * button, .modal-footer .btn-secondary')
    expect(wrapper.emitted('close')).toHaveLength(1)
    // The dialog is fading out; the run's own rows record the outcome.
    const state = () => (wrapper.vm as unknown as { rows: Array<{ result: string | null }> }).rows.map((row) => row.result ?? 'running')
    expect(state()).toEqual(['ok', 'running', 'running', 'running', 'running', 'skipped', 'skipped', 'skipped'])

    // A new batch right away does not open while the old one is busy.
    await wrapper.setProps({ action: null })
    await wrapper.setProps({ action: 'test', accounts: [{ id: 20, name: 'acc-20' }] })
    // (Dialogs that closed earlier stay in jsdom, their leave transitions never end.)
    const testConfirm = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-batch-confirm]'))
      .find((el) => el.textContent?.trim() === 'admin.schedQuality.action.test')
    expect(testConfirm()).toBeUndefined()

    for (const id of [2, 3, 4, 5]) answers.pending.get(id)!.resolve({ ...resetFixture, failed: [] })
    await flushPromises()
    expect(api.reset).toHaveBeenCalledTimes(5)
    expect(state()).toEqual(['ok', 'running', 'running', 'running', 'running', 'skipped', 'skipped', 'skipped'])
    expect(wrapper.emitted('busy')).toEqual([[true], [false]])
    expect(wrapper.emitted('done')).toHaveLength(1)

    // Now the next batch opens and runs on its own accounts only.
    await wrapper.setProps({ action: null })
    await wrapper.setProps({ action: 'test', accounts: [{ id: 20, name: 'acc-20' }] })
    testConfirm()!.click()
    await flushPromises()
    expect(api.startTest.mock.calls.map(([id]) => id)).toEqual([20])
    expect(api.reset).toHaveBeenCalledTimes(5)
    expect(results().slice(-1)).toEqual([['acc-20', 'admin.schedQuality.result.ok']])
  })
})
