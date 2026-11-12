import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedAccountDetail } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import { closeSchedDrawer, openSchedDrawer } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), startTest: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => !key.endsWith('.future_kind')
  })
}))

enableAutoUnmount(afterEach)

// The contract example plus a second group on its own window: TTFT from tests, error real, cache cooling without data.
function detail(): SchedAccountDetail {
  const d = structuredClone(detailFixture) as SchedAccountDetail
  d.groups.push({
    group_id: 54, name: '北辰组', gate_enabled: true, policy: d.groups[0].policy, override: null,
    by_model: [{
      model: 'gpt-6.1-sol', state: 'cooling', level: 1, reason: 'cache', since: '2026-11-11T12:00:00Z', until: null, waiting_test: true, testing: false,
      ttfo: { verdict: 'fail', from: 'test', n: 4, p90_ms: 14200, p90_timeout: false, limit_ms: 10000 },
      error: { verdict: 'pass', from: 'real', n: 30, rate: 0.02, limit: 0.05 },
      cache: { verdict: 'nodata', from: null, n: 3, ratio: null, limit: 0.9 }
    }]
  })
  return d
}

const panel = () => document.querySelector<HTMLElement>('[data-drawer-panel]')!
const row = (groupId: number) => document.querySelector<HTMLElement>(`[data-sq-group-row="${groupId}"]`)!
const cells = (groupId: number) => Array.from(row(groupId).querySelectorAll('td')).slice(2, 5)

beforeEach(() => {
  closeSchedDrawer()
  document.documentElement.dataset.motion = 'reduced'
  Element.prototype.scrollIntoView = vi.fn()
  api.accountDetail.mockReset().mockResolvedValue(detail())
})

afterEach(() => {
  document.querySelectorAll('body > button').forEach((el) => el.remove())
})

afterAll(() => {
  delete document.documentElement.dataset.motion
})

describe('SchedAccountDrawer', () => {
  it('loads the account on open, picks the requested model tab and marks the group row it was opened for', async () => {
    mount(SchedAccountDrawer, { attachTo: document.body })
    expect(document.querySelector('[data-drawer-panel]')).toBeNull()
    openSchedDrawer({ id: 479, name: 'acc-03' }, { model: 'gpt-6.1-sol', groupId: 53 })
    await flushPromises()
    expect(document.querySelector('[data-sq-drawer-loading]')).toBeNull()
    expect(api.accountDetail).toHaveBeenCalledWith(479, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(panel().querySelector('[role="tab"][aria-selected="true"]')!.textContent).toBe('gpt-6.1-sol')
    expect(Array.from(panel().querySelectorAll('.sq-tile .text-2xl')).map((el) => el.textContent)).toEqual(['12.4s', '0.5%', '81%'])
    expect(row(53).classList.contains('sq-target')).toBe(true)
    expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'center' })
  })

  it('falls back to the first tab when the detail has no data for the requested model', async () => {
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' }, { model: 'gpt-9' })
    await flushPromises()
    expect(panel().querySelector('[role="tab"][aria-selected="true"]')!.textContent).toBe('gpt-6.1-sol')
  })

  it('colours group rows by the verdict from the API: shapes on the cards window, own numbers otherwise, test values as given', async () => {
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()

    // Same window as the cards: shapes only, following each dimension's verdict.
    expect(cells(53).map((td) => td.querySelector('.sq-shape')!.className)).toEqual([
      'sq-shape sq-shape--fail', 'sq-shape sq-shape--pass', 'sq-shape sq-shape--fail'
    ])
    expect(cells(53).map((td) => td.querySelector('.sq-shape')!.getAttribute('aria-label'))).toEqual([
      'admin.schedQuality.dim.ttfo admin.schedQuality.aria.fail',
      'admin.schedQuality.dim.error admin.schedQuality.aria.pass',
      'admin.schedQuality.dim.cache admin.schedQuality.aria.fail'
    ])

    // Own window: the test value given by the API, a real number, and the cause square for the cooling dimension.
    const [ttfo, error, cache] = cells(54)
    expect(ttfo.textContent).toBe('14.2s')
    expect(ttfo.querySelector('span')!.className).toContain('sq-v-fail')
    expect(error.textContent).toBe('2.0%')
    expect(error.querySelector('span')!.className).toContain('sq-v-pass')
    expect(cache.querySelector('.sq-shape')!.className).toContain('sq-shape--fail')
    expect(row(54).querySelector('td:nth-child(2)')!.textContent).toBe('admin.schedQuality.state.cooling')
  })

  it('keeps focus in the drawer when a refresh removes the focused control', async () => {
    const wrapper = mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    const clear = panel().querySelector<HTMLElement>('[data-sq-trigger="auto:model_limit:gpt-6-astra"]')!
    clear.focus()
    expect(document.activeElement).toBe(clear)

    // Re-rendered with the same control: focus stays on it.
    await (wrapper.vm as unknown as { refresh: () => Promise<void> }).refresh()
    await flushPromises()
    expect(document.activeElement?.getAttribute('data-sq-trigger')).toBe('auto:model_limit:gpt-6-astra')

    // The state is gone: focus falls back to the drawer panel, never to the page.
    api.accountDetail.mockResolvedValue({ ...detail(), auto_states: [] })
    await (wrapper.vm as unknown as { refresh: () => Promise<void> }).refresh()
    await flushPromises()
    expect(document.activeElement).toBe(panel())
  })

  it('closes on Escape and gives focus back to the number that opened it', async () => {
    mount(SchedAccountDrawer, { attachTo: document.body })
    const opener = document.createElement('button')
    opener.setAttribute('data-sq-trigger', 'num:479:ttfo')
    document.body.appendChild(opener)
    opener.focus()
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    expect(document.activeElement).toBe(panel())
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(document.querySelector('[data-drawer-panel]')).toBeNull()
    expect(document.activeElement).toBe(opener)
  })
})
