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
      return resetFixture
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
})
