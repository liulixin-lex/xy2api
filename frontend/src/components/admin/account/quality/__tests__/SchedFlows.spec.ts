import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach } from 'vitest'

import SchedBatchDialog from '../SchedBatchDialog.vue'

const api = vi.hoisted(() => ({ reset: vi.fn(), setOverride: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ manage: true })
})
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => perms.manage) }) }
})
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
  perms.manage = true
  Object.values(api).forEach((fn) => fn.mockReset())
  document.documentElement.dataset.motion = 'reduced'
})
afterAll(() => { delete document.documentElement.dataset.motion })

const click = async (selector: string) => {
  document.querySelector<HTMLElement>(selector)!.click()
  await flushPromises()
}

describe('batch flow', () => {
  const accounts = [{ id: 1, name: 'acc-a' }, { id: 2, name: 'acc-b' }, { id: 3, name: 'acc-c' }]
  const results = () => Array.from(document.querySelectorAll('[data-sq-batch-row]')).map((tr) => [
    tr.querySelector('td')!.textContent, tr.querySelector('td:last-child')!.textContent?.trim()
  ])

  it('confirms a batch recovery with the account count and 取消 / 确定, then hands the accounts back without a result table (F3)', async () => {
    const wrapper = mount(SchedBatchDialog, { props: { action: 'reset', accounts, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    const dialog = document.querySelector<HTMLElement>('[data-sq-batch-confirm]')!.closest('.modal-content')!
    expect(dialog.textContent).toContain('admin.schedQuality.action.reset')
    expect(Array.from(dialog.querySelectorAll('p span')).map((el) => el.textContent)).toEqual(['admin.schedQuality.col.account', '3'])
    expect(Array.from(dialog.querySelectorAll('.modal-footer button')).map((b) => b.textContent?.trim()))
      .toEqual(['admin.schedQuality.action.cancel', 'admin.schedQuality.action.confirm'])

    await click('[data-sq-batch-confirm]')
    expect(wrapper.emitted('recover')).toEqual([[[1, 2, 3]]])
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(api.reset).not.toHaveBeenCalled()
    expect(document.querySelector('[data-sq-batch-results]')).toBeNull()
  })

  it('closes an unconfirmed batch recovery when accounts:manage is withdrawn, and sends nothing (F3, scenario 41)', async () => {
    const wrapper = mount(SchedBatchDialog, { props: { action: 'reset', accounts, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    perms.manage = false
    await flushPromises()
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.emitted('recover')).toBeUndefined()
    expect(api.reset).not.toHaveBeenCalled()
  })

  // One answer per account, released by the test.
  function deferred() {
    const pending = new Map<number, { resolve: (value: unknown) => void; reject: (error: unknown) => void }>()
    const call = (id: number) => new Promise((resolve, reject) => { pending.set(id, { resolve, reject }) })
    return { pending, call }
  }

  it('keeps the action, request and accounts it started with when the props change mid-run', async () => {
    const answers = deferred()
    api.setOverride.mockImplementation((id: number) => answers.call(id))
    const many = Array.from({ length: 6 }, (_, i) => ({ id: i + 1, name: `acc-${i + 1}` }))
    const wrapper = mount(SchedBatchDialog, { props: { action: 'exempt', accounts: many, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    // The popover confirms with an empty reason: it is sent as is (contract clarification K22).
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    const request = api.setOverride.mock.calls[0][1]
    expect(request).toMatchObject({ group_id: 0, mode: 'exempt', reason: '' })
    expect(api.setOverride).toHaveBeenCalledTimes(4)

    await wrapper.setProps({ action: 'reset', accounts: [{ id: 99, name: 'other' }] })
    for (const id of [1, 2, 3, 4]) answers.pending.get(id)!.resolve(undefined)
    await flushPromises()
    for (const id of [5, 6]) answers.pending.get(id)!.resolve(undefined)
    await flushPromises()
    expect(api.setOverride.mock.calls.map(([id, body]) => [id, body])).toEqual(many.map(({ id }) => [id, request]))
    expect(results()).toEqual(many.map(({ name }) => [name, 'admin.schedQuality.result.ok']))
  })

  it('stops at close: later accounts are not run, late answers are dropped, and no batch starts until they are in', async () => {
    const answers = deferred()
    api.setOverride.mockImplementation((id: number) => answers.call(id))
    const many = Array.from({ length: 8 }, (_, i) => ({ id: i + 1, name: `acc-${i + 1}` }))
    const wrapper = mount(SchedBatchDialog, { props: { action: 'exempt', accounts: many, anchor: null, groups: [] }, attachTo: document.body })
    await flushPromises()
    await click('[data-sq-popover] .btn-primary')
    expect(wrapper.emitted('busy')).toEqual([[true]])
    answers.pending.get(1)!.resolve(undefined)
    await flushPromises()
    expect(api.setOverride).toHaveBeenCalledTimes(5)

    // Close the results while 2, 3, 4 and 5 are on their way.
    await click('[data-sq-batch-results] ~ * button, .modal-footer .btn-secondary')
    expect(wrapper.emitted('close')).toHaveLength(1)
    // The dialog is fading out; the run's own rows record the outcome.
    const state = () => (wrapper.vm as unknown as { rows: Array<{ result: string | null }> }).rows.map((row) => row.result ?? 'running')
    expect(state()).toEqual(['ok', 'running', 'running', 'running', 'running', 'skipped', 'skipped', 'skipped'])

    // A new batch right away does not open while the old one is busy.
    await wrapper.setProps({ action: null })
    await wrapper.setProps({ action: 'reset', accounts: [{ id: 20, name: 'acc-20' }] })
    // (Dialogs that closed earlier stay in jsdom, their leave transitions never end.)
    const resetConfirm = () => document.querySelector<HTMLElement>('[data-sq-batch-confirm]')
    expect(resetConfirm()).toBeNull()

    for (const id of [2, 3, 4, 5]) answers.pending.get(id)!.resolve(undefined)
    await flushPromises()
    expect(api.setOverride).toHaveBeenCalledTimes(5)
    expect(state()).toEqual(['ok', 'running', 'running', 'running', 'running', 'skipped', 'skipped', 'skipped'])
    expect(wrapper.emitted('busy')).toEqual([[true], [false]])
    expect(wrapper.emitted('done')).toHaveLength(1)

    // Now the next batch opens and works on its own accounts only.
    await wrapper.setProps({ action: null })
    await wrapper.setProps({ action: 'reset', accounts: [{ id: 20, name: 'acc-20' }] })
    resetConfirm()!.click()
    await flushPromises()
    expect(wrapper.emitted('recover')).toEqual([[[20]]])
    expect(api.setOverride).toHaveBeenCalledTimes(5)
  })
})
