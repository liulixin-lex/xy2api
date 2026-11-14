import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { BackendAccountDetail, BackendDimDetail, BackendGroupModel, BackendPolicy } from '@/api/admin/schedQualityTypes'
import SchedQualityPanel from '../SchedQualityPanel.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import type { LiveTable } from '@/composables/useLiveTable'
import { withSchedHost } from './schedLayerHost'

const { api, backendSummary } = vi.hoisted(() => ({
  api: { accountDetail: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn(), reset: vi.fn() },
  backendSummary: vi.fn()
}))
vi.mock('@/api/admin/schedQuality', () => ({ default: api, backendSummary }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => true) }) }
})
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: () => true
  })
}))

enableAutoUnmount(afterEach)

const policy: BackendPolicy = { enabled: true, ttft_seconds: 10, max_error_rate: 0.05, min_cache_ratio: 0.5, takeover_idle_seconds: 0 }
const dim = (verdict: string, n = 40): BackendDimDetail & { bad: number } => ({ scope: 'own', window_min: 60, n, lcb: null, verdict, bad: 0 })
const inMinutes = (m: number) => new Date(Date.now() + m * 60_000).toISOString()
const model = (name: string, failing = false): BackendGroupModel => ({
  model: name,
  failing,
  dims: failing ? ['ttfo'] : [],
  level: failing ? 1 : 0,
  since: null,
  until: failing ? inMinutes(20) : null,
  other_group_ids: [],
  exempt: false,
  ttfo: dim(failing ? 'fail' : 'pass'),
  error: dim('pass'),
  cache: { ...dim('short', 3), ratio: null }
})

// Two groups; gpt-b is at the back in 晨星组. The IQ gate of each model has its own recheck time.
function detail(id = 7): BackendAccountDetail {
  return {
    account: { id, name: `acc-${id}`, priority: 2, rate: 1, concurrency: 10, in_flight: 1 },
    at: '2026-11-14T10:00:00Z',
    stale: false,
    global_enabled: true,
    status: 'normal',
    stop: null,
    reset_at: null,
    groups: [
      { group_id: 53, name: '晨星组', policy, exemption: null, by_model: [model('gpt-a'), model('gpt-b', true)] },
      { group_id: 54, name: '北辰组', policy, exemption: null, by_model: [model('gpt-a')] }
    ],
    auto_states: [
      { key: 'iq:gpt-a', kind: 'iq_gate', scope: 'model', model: 'gpt-a', until: inMinutes(5), count: null, shared: 0, clearable: false, why: 'iq_gate' },
      { key: 'iq:gpt-b', kind: 'iq_gate', scope: 'model', model: 'gpt-b', until: inMinutes(42), count: null, shared: 0, clearable: false, why: 'iq_gate' }
    ],
    exemptions: []
  }
}

const panel = () => document.querySelector<HTMLElement>('[data-quality-panel]')
const tab = (name: string) => document.querySelector<HTMLElement>(`[data-qp-tab="${name}"]`)!
const groupRow = (id: number) => document.querySelector<HTMLElement>(`[data-qp-group-row="${id}"]`)!
const pointerDown = (el: Element) => el.dispatchEvent(new Event('pointerdown', { bubbles: true, cancelable: true }))
const outside = (attrs: Record<string, string> = {}, onClick = vi.fn()) => {
  const button = document.createElement('button')
  Object.entries(attrs).forEach(([name, value]) => button.setAttribute(name, value))
  button.addEventListener('click', onClick)
  document.body.appendChild(button)
  return { button, onClick }
}

// The panel inside a page whose live table fetches its data (the accounts and groups pages).
const Panel = withSchedHost(SchedQualityPanel)
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => { resolve = r })
  return { promise, resolve }
}

async function openOn(id = 7, options: { groupId?: number; model?: string } = {}) {
  openSchedDrawer({ id, name: `acc-${id}` }, options)
  await flushPromises()
}

beforeEach(() => {
  closeSchedDrawer()
  closeSchedBoard()
  document.body.innerHTML = ''
  document.documentElement.dataset.motion = 'reduced'
  api.accountDetail.mockReset().mockImplementation(async (id: number) => detail(id))
  api.setOverride.mockReset().mockResolvedValue(undefined)
  backendSummary.mockReset().mockResolvedValue({ at: '', stale: false, global_enabled: true, accounts: [] })
})

afterAll(() => {
  delete document.documentElement.dataset.motion
})

describe('SchedQualityPanel data from the page\'s live table (7f-1)', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('asks nothing while closed, at once on opening, then on the table\'s 5 s beat, and nothing while the table is paused', async () => {
    vi.useFakeTimers()
    let table!: LiveTable<{ id: number }>
    mount(withSchedHost(SchedQualityPanel, { onTable: (value) => { table = value } }), { attachTo: document.body })
    await vi.advanceTimersByTimeAsync(12_000)
    expect(api.accountDetail).not.toHaveBeenCalled()

    openSchedDrawer({ id: 7, name: 'acc-7' })
    await vi.advanceTimersByTimeAsync(0)
    expect(api.accountDetail).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5000)
    expect(api.accountDetail).toHaveBeenCalledTimes(2)

    table.pause()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(api.accountDetail).toHaveBeenCalledTimes(2)
    table.resume()
    await vi.advanceTimersByTimeAsync(0)
    expect(api.accountDetail).toHaveBeenCalledTimes(3)
  })

  it('drops the answer for an account that is no longer open', async () => {
    mount(Panel, { attachTo: document.body })
    const late = deferred<BackendAccountDetail>()
    const current = deferred<BackendAccountDetail>()
    api.accountDetail.mockReturnValueOnce(late.promise).mockReturnValueOnce(current.promise)
    await openOn(7)
    await openOn(8)
    late.resolve(detail(7))
    await flushPromises()
    expect(panel()!.querySelector('[data-qp-loading]')).not.toBeNull()
    current.resolve(detail(8))
    await flushPromises()
    expect(panel()!.querySelector('.qp-meta')).not.toBeNull()
    expect(panel()!.querySelector('h2')!.textContent).toBe('acc-8')
  })

  it('backs off with the table after a failure: the page goes stale, and the panel offers a retry', async () => {
    let table!: LiveTable<{ id: number }>
    mount(withSchedHost(SchedQualityPanel, { onTable: (value) => { table = value } }), { attachTo: document.body })
    api.accountDetail.mockRejectedValue(new Error('down'))
    await openOn(7)
    expect(table.status.value).toBe('stale')
    expect(panel()!.querySelector('[data-qp-failed]')).not.toBeNull()
    api.accountDetail.mockImplementation(async (id: number) => detail(id))
    panel()!.querySelector<HTMLElement>('[data-qp-failed] button')!.click()
    await flushPromises()
    expect(panel()!.querySelector('.qp-meta')).not.toBeNull()
    expect(table.status.value).toBe('live')
  })
})

describe('SchedQualityPanel switching accounts (7h)', () => {
  it('keeps the retry within reach when the new account fails to load (7h-1)', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7)
    api.accountDetail.mockRejectedValueOnce(new Error('down'))
    await openOn(8)
    const body = panel()!.querySelector<HTMLElement>('[data-qp-body]')!
    expect(body.hasAttribute('inert')).toBe(false)
    expect(body.classList.contains('opacity-60')).toBe(false)
    panel()!.querySelector<HTMLElement>('[data-qp-failed] button')!.click()
    await flushPromises()
    expect(api.accountDetail).toHaveBeenLastCalledWith(8, expect.anything())
    expect(panel()!.querySelector('.qp-meta')).not.toBeNull()
  })

  it('closes the exemption popover when the panel switches to another account (7h-2)', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7)
    groupRow(54).querySelector<HTMLElement>('[data-qp-trigger="exempt:54"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-sq-popover]')).not.toBeNull()
    await openOn(8)
    expect(document.querySelector('[data-sq-popover]')).toBeNull()
    expect(api.setOverride).not.toHaveBeenCalled()
  })

  it('moves focus to the title when it goes to the parent account from inside (7h-3)', async () => {
    api.accountDetail.mockImplementation(async (id: number) => ({
      ...detail(id),
      auto_states: [{ key: 'parent_blocked:12', kind: 'parent_blocked', scope: 'account', model: '', until: null, count: null, shared: 0, clearable: false, why: 'parent_blocked' }]
    }))
    mount(Panel, { attachTo: document.body })
    await openOn(7)
    document.querySelector<HTMLElement>('[data-qp-trigger="goto:parent_blocked:12"]')!.click()
    await flushPromises()
    expect(api.accountDetail).toHaveBeenLastCalledWith(12, expect.anything())
    expect(document.activeElement).toBe(panel()!.querySelector('h2'))
  })
})

describe('SchedQualityPanel', () => {
  it('opened from a dot: picks the tab of the model the group is at the back for, and marks that group row', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    expect(api.accountDetail).toHaveBeenCalledWith(7, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(tab('gpt-b').getAttribute('aria-selected')).toBe('true')
    expect(groupRow(53).classList.contains('is-target')).toBe(true)
    expect(groupRow(54).classList.contains('is-target')).toBe(false)
    expect(groupRow(53).querySelector('[data-test="qp-state"]')!.textContent).toContain('admin.accounts.table.dotStates.back')
    // The amber mark sits inside the tab of the model with a group at the back, before its text.
    expect(tab('gpt-b').firstElementChild!.hasAttribute('data-qp-tab-back')).toBe(true)
    expect(tab('gpt-a').querySelector('[data-qp-tab-back]')).toBeNull()

    // Another group's dot of the same account without a substandard model keeps the tab shown.
    await openOn(7, { groupId: 54 })
    expect(tab('gpt-b').getAttribute('aria-selected')).toBe('true')
    expect(groupRow(54).classList.contains('is-target')).toBe(true)
  })

  it('closes on a pointerdown outside it and lets that click go on', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn()
    const { button, onClick } = outside()
    expect(pointerDown(button)).toBe(true) // not cancelled
    button.click()
    await flushPromises()
    expect(panel()).toBeNull()
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('switches its content in place when another dot is pressed: the new name at once, the old body dimmed, no skeleton (7f-4)', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    const before = panel()
    const next = deferred<BackendAccountDetail>()
    api.accountDetail.mockReturnValueOnce(next.promise)
    const { button } = outside({ 'data-quality-opener': '8:54' }, vi.fn(() => openSchedDrawer({ id: 8, name: 'acc-8' }, { groupId: 54 })))
    pointerDown(button)
    button.click()
    await flushPromises()
    expect(panel()).toBe(before)
    expect(api.accountDetail).toHaveBeenLastCalledWith(8, expect.anything())
    // Before the new detail: the header is acc-8's, the body still acc-7's, dimmed and out of reach; no skeleton.
    expect(panel()!.querySelector('h2')!.textContent).toBe('acc-8')
    expect(panel()!.querySelector('.qp-meta')).toBeNull()
    expect(panel()!.querySelector('[data-qp-loading]')).toBeNull()
    const body = panel()!.querySelector<HTMLElement>('[data-qp-body]')!
    expect(body.classList.contains('opacity-60')).toBe(true)
    expect(body.hasAttribute('inert')).toBe(true)
    expect(tab('gpt-b').getAttribute('aria-selected')).toBe('true')

    next.resolve({ ...detail(8), groups: [{ ...detail(8).groups[1] }] })
    await flushPromises()
    expect(body.classList.contains('opacity-60')).toBe(false)
    expect(body.hasAttribute('inert')).toBe(false)
    expect(panel()!.querySelector('.qp-meta')).not.toBeNull()
    expect(tab('gpt-a').getAttribute('aria-selected')).toBe('true')
    expect(groupRow(54).classList.contains('is-target')).toBe(true)
  })

  it('shows the skeleton only when it opens with nothing to show yet', async () => {
    mount(Panel, { attachTo: document.body })
    const first = deferred<BackendAccountDetail>()
    api.accountDetail.mockReturnValueOnce(first.promise)
    await openOn(7)
    expect(panel()!.querySelector('[data-qp-loading]')).not.toBeNull()
    first.resolve(detail(7))
    await flushPromises()
    expect(panel()!.querySelector('[data-qp-loading]')).toBeNull()

    // Closed and opened again: what it showed before is gone.
    closeSchedDrawer()
    await flushPromises()
    api.accountDetail.mockReturnValueOnce(new Promise(() => {}))
    await openOn(7)
    expect(panel()!.querySelector('[data-qp-loading]')).not.toBeNull()
  })

  it('stays open for clicks in the exemption popover it opened, then reloads and tells the page', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7)
    const { changed } = useSchedLayers()
    const changes = changed.value
    groupRow(54).querySelector<HTMLElement>('[data-qp-trigger="exempt:54"]')!.click()
    await flushPromises()
    const popover = document.querySelector<HTMLElement>('[data-sq-popover]')!
    expect(popover).not.toBeNull()
    pointerDown(popover)
    await flushPromises()
    expect(panel()).not.toBeNull()

    api.accountDetail.mockClear()
    Array.from(popover.querySelectorAll('button')).find((el) => el.textContent?.includes('admin.schedQuality.action.confirm'))!.click()
    await flushPromises()
    expect(api.setOverride).toHaveBeenCalledWith(7, expect.objectContaining({ group_id: 54, mode: 'exempt' }))
    expect(api.accountDetail).toHaveBeenCalledTimes(1)
    expect(changed.value).toBe(changes + 1)
    expect(panel()).not.toBeNull()
  })

  it('closes on Escape or the close button and gives focus back to the dot that opened it', async () => {
    mount(Panel, { attachTo: document.body })
    const { button: dot } = outside({ 'data-quality-opener': '7:53' })
    await openOn(7, { groupId: 53 })
    tab('gpt-a').focus()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await flushPromises()
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(dot)

    await openOn(7, { groupId: 53 })
    dot.blur()
    panel()!.querySelector<HTMLElement>('[data-qp-close]')!.click()
    await flushPromises()
    expect(panel()).toBeNull()
    expect(document.activeElement).toBe(dot)
  })

  it('lists every model\'s own recheck time in the automatic states', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn()
    const times = ['iq:gpt-a', 'iq:gpt-b'].map((key) => document.querySelector(`[data-qp-auto="${key}"] .arow__t`)!.textContent)
    expect(times[0]).toMatch(/^admin\.schedQuality\.panel\.autoRecheck:\{"time":"[45]:\d\d"\}$/)
    expect(times[1]).toMatch(/^admin\.schedQuality\.panel\.autoRecheck:\{"time":"4[12]:\d\d"\}$/)
  })

  it('puts its top edge on the table\'s top, never above the page header', async () => {
    const table = document.createElement('div')
    document.body.appendChild(table)
    let tableTop = 180
    table.getBoundingClientRect = () => ({ top: tableTop }) as DOMRect
    mount(Panel, { attachTo: document.body, props: { anchor: () => table } })
    await openOn()
    expect(panel()!.style.top).toBe('180px')

    closeSchedDrawer()
    await flushPromises()
    tableTop = 20
    await openOn()
    expect(panel()!.style.top).toBe('64px')
  })

  it('reads the recent values from the page\'s row when it has one, and asks for the summary itself otherwise', async () => {
    const alone = mount(Panel, { attachTo: document.body })
    await openOn()
    expect(backendSummary).toHaveBeenCalledWith([7], expect.anything())

    closeSchedDrawer()
    await flushPromises()
    alone.unmount()
    backendSummary.mockClear()
    const quality = {
      account_id: 7, status: 'normal', reset_at: null, stop: null, substandard: [], exemptions: [],
      ttfo: { n: 40, short: false, red: true, p50_ms: 12_300 }, error: { n: 40, short: false, red: false, rate: 0.01 }, cache: { n: 2, short: true, red: false, ratio: 0.4 }
    }
    const row = { id: 7, name: 'acc-7', platform: 'openai', type: 'oauth', quality } as never
    mount(withSchedHost(SchedQualityPanel, { row: () => row }), { attachTo: document.body, props: { account: () => row } })
    await openOn()
    expect(backendSummary).not.toHaveBeenCalled()
    const value = (key: string) => document.querySelector(`[data-qp-recent="${key}"] .rrow__v`)!
    expect(value('ttfo').textContent).toBe('12.3s')
    expect(value('ttfo').classList.contains('rrow__v--over')).toBe(true)
    expect(value('cache').classList.contains('rrow__v--na')).toBe(true)
    expect(document.querySelector('[data-qp-recent="ttfo"] .rrow__g')!.textContent).toContain('"value":"≤ 10.0s"')
  })
})
