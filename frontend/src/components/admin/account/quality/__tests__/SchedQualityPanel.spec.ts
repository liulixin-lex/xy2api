import { afterAll, afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { BackendAccountDetail, BackendDecision, BackendGroupModel, BackendPolicy } from '@/api/admin/schedQualityTypes'
import { basis, errorEvidence, groupModel, legacyBasis, legacyLine, line, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import { lastEval, perfAnswer, perfGroup, perfRow } from '@/__tests__/fixtures/schedQuality/performance'
import SchedQualityPanel from '../SchedQualityPanel.vue'
import SchedDecisions from '../SchedDecisions.vue'
import decisionsSource from '../SchedDecisions.vue?raw'
import panelPerfSource from '../SchedPanelPerformance.vue?raw'
import SchedEvidence from '../SchedEvidence.vue'
import { closeSchedBoard, closeSchedDrawer, markSchedChanged, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import type { LiveTable } from '@/composables/useLiveTable'
import { withSchedHost } from './schedLayerHost'

const { api, backendSummary } = vi.hoisted(() => ({
  api: { accountDetail: vi.fn(), accountDecisions: vi.fn(), accountPerformance: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn(), reset: vi.fn() },
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
const inMinutes = (m: number) => new Date(Date.now() + m * 60_000).toISOString()
/**
 * Freezes Date alone (timers stay real) at local noon until the test ends: times shown as today's HH:MM stay today when
 * the run starts near midnight (a run at 23:45 read "now + 20 minutes" as 11-20 00:05).
 */
const atLocalNoon = () => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 10, 19, 12, 0))
  onTestFinished(() => {
    vi.useRealTimers()
  })
}
/** A model row: first output at the rear for 20 more minutes (level 1, a 40-minute stay) when `rear`; cache unknown. */
const model = (name: string, rear = false): BackendGroupModel => {
  const row = groupModel(name, rear ? { ttfo: 'rear', cache: 'unknown' } : { cache: 'unknown' })
  if (rear) Object.assign(row.lines.ttfo, { level: 1, until: inMinutes(20) })
  return row
}

// Two groups; gpt-b is at the back in 晨星组. The IQ gate of each model has its own recheck time.
function detail(id = 7): BackendAccountDetail {
  return {
    account: { id, name: `acc-${id}`, priority: 2, rate: 1, concurrency: 10, in_flight: 1 },
    at: '2026-11-14T10:00:00Z',
    stale: false,
    global_enabled: true,
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
  api.accountDecisions.mockReset().mockResolvedValue([])
  api.accountPerformance.mockReset().mockResolvedValue(perfAnswer([]))
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
    api.accountDetail.mockRejectedValue({ status: 500, message: 'account detail unavailable' })
    await openOn(7)
    expect(table.status.value).toBe('stale')
    // "Failed", and why in the backend's words (7n-4).
    expect(panel()!.querySelector('[data-qp-failed-reason]')!.textContent).toBe('account detail unavailable')
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

  it('names the parent in the title at once when the page knows it, before its detail is in (7l-9)', async () => {
    const parentDetail = deferred<BackendAccountDetail>()
    api.accountDetail.mockImplementation(async (id: number) => (id === 12 ? parentDetail.promise : {
      ...detail(id),
      auto_states: [{ key: 'parent_blocked:12', kind: 'parent_blocked', scope: 'account', model: '', until: null, count: null, shared: 0, clearable: false, why: 'parent_blocked' }]
    }))
    const parent = { id: 12, name: 'parent-12', platform: 'openai', type: 'oauth' } as never
    mount(Panel, { attachTo: document.body, props: { account: (id: number) => (id === 12 ? parent : undefined) } })
    await openOn(7)
    document.querySelector<HTMLElement>('[data-qp-trigger="goto:parent_blocked:12"]')!.click()
    await flushPromises()
    expect(panel()!.querySelector('h2')!.textContent).toBe('parent-12')
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
    expect(groupRow(53).querySelector('[data-test="qp-state"]')!.textContent).toContain('admin.schedQuality.placement.rear')
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

  it('moves no focus when a press outside closes it, so nothing scrolls under the pointer (7q-1)', async () => {
    outside({ 'data-quality-opener': '7:53' }).button.focus()
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53 })
    panel()!.querySelector<HTMLElement>('[data-qp-close]')!.focus()
    // Giving focus back is what scrolled the table (focus() scrolls its target into view): none at all.
    const focus = vi.spyOn(HTMLElement.prototype, 'focus')
    onTestFinished(() => focus.mockRestore())

    pointerDown(outside().button)
    await flushPromises()
    expect(panel()).toBeNull()
    expect(focus).not.toHaveBeenCalled()
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

  it('takes the whole screen below 1024px, from the very top', async () => {
    const narrow = vi.spyOn(window, 'matchMedia').mockImplementation(
      (query) => ({ matches: false, media: query, onchange: null, addEventListener: vi.fn(), removeEventListener: vi.fn(), addListener: vi.fn(), removeListener: vi.fn(), dispatchEvent: vi.fn() }) as MediaQueryList
    )
    onTestFinished(() => narrow.mockRestore())
    const table = document.createElement('div')
    document.body.appendChild(table)
    table.getBoundingClientRect = () => ({ top: 180 }) as DOMRect
    mount(Panel, { attachTo: document.body, props: { anchor: () => table } })
    await openOn()
    expect(panel()!.style.top).toBe('0px')
  })

  it('is modal while it takes the whole screen, and goes back to non-modal from 1024px while open (7l-2)', async () => {
    let wide = false
    const listeners: Array<(event: { matches: boolean }) => void> = []
    const media = vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      get matches() { return query.includes('min-width: 1024px') ? wide : true },
      media: query,
      onchange: null,
      addEventListener: (_type: string, listener: (event: { matches: boolean }) => void) => listeners.push(listener),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn()
    }) as unknown as MediaQueryList)
    onTestFinished(() => media.mockRestore())
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(panel()!.getAttribute('aria-modal')).toBe('true')
    expect(document.activeElement).toBe(panel()!.querySelector('.qp-name'))
    expect(document.body.classList.contains('modal-open')).toBe(true)
    // Tab from the last control comes back to the first.
    const controls = Array.from(panel()!.querySelectorAll<HTMLElement>('button:not([disabled]), [tabindex="0"]'))
    controls.at(-1)!.focus()
    controls.at(-1)!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
    expect(document.activeElement).toBe(controls[0])

    wide = true
    listeners.forEach((listener) => listener({ matches: true }))
    await flushPromises()
    expect(panel()!.getAttribute('aria-modal')).toBe('false')
    expect(document.body.classList.contains('modal-open')).toBe(false)
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
      account_id: 7, reset_at: null, stop: null, rear: [], exemptions: [],
      ttfo: { first_event_p50_ms: 1100, first_output_p50_ms: 12_300, gap_p50_ms: 11_200, n: 40 },
      error: { n: 40, short: false, red: false, rate: 0.01 },
      cache: { ratio: 0.4, turns: 2, all_ratio: 0.7, all_requests: 90 }
    }
    const row = { id: 7, name: 'acc-7', platform: 'openai', type: 'oauth', quality } as never
    mount(withSchedHost(SchedQualityPanel, { row: () => row }), { attachTo: document.body, props: { account: () => row } })
    await openOn()
    expect(backendSummary).not.toHaveBeenCalled()
    const value = (key: string) => document.querySelector(`[data-qp-recent="${key}"] .rrow__v`)!
    // First token as the line judges it (first useful output), the first event and the gap under it; cache over the
    // continuation turns (muted: under 20) and over every successful request (N14).
    expect(value('ttfo').textContent).toBe('12.3s')
    expect(value('ttfo_event').textContent).toBe('1.1s')
    expect(document.querySelector('[data-qp-recent="ttfo_event"] .rrow__g')!.textContent).toBe('admin.schedQuality.panel.gap:{"value":"11.2s"}')
    expect(value('cache').textContent).toBe('40%')
    expect(value('cache').classList.contains('rrow__v--na')).toBe(true)
    expect(value('cache_all').textContent).toBe('70%')
    expect(value('cache_all').classList.contains('rrow__v--na')).toBe(false)
    // The whole account's value beside the strictest groups' threshold: no "over the line", no amber, a note that the
    // threshold judges per group and model (7k-1).
    expect(value('ttfo').className).toBe('rrow__v')
    expect(document.querySelector('[data-qp-recent="ttfo"] .rrow__g')!.textContent).toBe('admin.schedQuality.panel.threshold:{"value":"≤ 10.0s"}')
    expect(document.querySelector('[data-test="qp-threshold-note"]')!.textContent).toBe('admin.schedQuality.panel.thresholdNote')
  })

  it('shows each line\'s identity: unknown in grey with 证据不够, trial with its time left, rear with its stay and end', async () => {
    atLocalNoon()
    const value = detail()
    const row = value.groups[0].by_model[1]
    row.lines.error = line('error', 'trial', { trial_until: inMinutes(30) })
    row.lines.cache = line('cache', 'unknown')
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    const marks = Array.from(groupRow(53).querySelectorAll<HTMLElement>('[data-qp-mark]'))
    expect(marks.map((mark) => mark.dataset.identity)).toEqual(['rear', 'trial', 'unknown'])
    expect(marks.map((mark) => mark.className)).toEqual(['mk mk--rear', 'mk mk--trial', 'mk mk--unknown'])
    const tips = marks.map((mark) => mark.getAttribute('aria-label'))
    expect(tips[0]).toMatch(/^admin\.schedQuality\.dim\.ttfo · admin\.schedQuality\.identity\.rear · admin\.schedQuality\.identity\.rearUntil:\{"time":"\d\d:\d\d","minutes":40\}$/)
    expect(tips[1]).toMatch(/^admin\.schedQuality\.dim\.error · admin\.schedQuality\.identity\.trial · admin\.schedQuality\.identity\.trialLeft:\{"time":"(29:59|30:00)"\}$/)
    expect(tips[2]).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.noAction.none')
    // The place the lines add up to: the rear, as long as the longest rear line.
    expect(groupRow(53).querySelector('[data-test="qp-state"]')!.textContent).toMatch(/admin\.schedQuality\.placement\.rear(19:59|20:00)$/)
    // A group without a row for the model: at the front, every line unknown.
    expect(groupRow(54).querySelector<HTMLElement>('[data-qp-place]')!.dataset.qpPlace).toBe('front')
    expect(Array.from(groupRow(54).querySelectorAll<HTMLElement>('[data-qp-mark]'), (mark) => mark.dataset.identity)).toEqual(['unknown', 'unknown', 'unknown'])
  })

  // A disabled button takes no focus and may get no mouse events: the reason is on its wrapper, which Tab reaches.
  it('says why 恢复 is disabled on the element around it when the account has nothing to recover', async () => {
    const tipOf = (selector: string) => (document.querySelector(selector) as unknown as { __tipText: string }).__tipText
    const wrapper = () => document.querySelector<HTMLElement>('[data-qp-recover]')!
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(document.querySelector<HTMLButtonElement>('[data-qp-trigger="reset"]')!.disabled).toBe(false)
    expect(tipOf('[data-qp-trigger="reset"]')).toBe('admin.schedQuality.panel.recoverTip')
    expect(tipOf('[data-qp-recover]')).toBe('')
    expect(wrapper().hasAttribute('tabindex')).toBe(false)

    closeSchedDrawer()
    await flushPromises()
    const calm = detail()
    calm.groups.forEach((group) => group.by_model.forEach((row) => { row.placement = 'front' }))
    api.accountDetail.mockResolvedValue(calm)
    await openOn()
    const button = document.querySelector<HTMLButtonElement>('[data-qp-trigger="reset"]')!
    expect(button.disabled).toBe(true)
    expect(button.parentElement).toBe(wrapper())
    expect(tipOf('[data-qp-recover]')).toBe('admin.schedQuality.panel.recoverNone')
    expect(wrapper().getAttribute('tabindex')).toBe('0')
    expect(tipOf('[data-qp-trigger="reset"]')).toBe('')
  })

  // Why a line is unknown besides the evidence (P9): this group's gate is off (not judged at all), or quality scheduling
  // is switched off (judging goes on, selection ignores the layers), or the view is stale.
  it('says the gate is off instead of any other reason: in one group (not judged), or switched off for everything (judged as usual)', async () => {
    const value = detail()
    value.groups[1].policy = { ...policy, enabled: false }
    value.groups[1].by_model[0].lines.ttfo = line('ttfo', 'unknown', { evidence: ttfoEvidence({ causes: ['mass_insufficient'] }) })
    // An assessment on record does not override the gate switched off.
    value.groups[1].by_model[0].lines.cache = line('cache', 'unknown', { last_eval: lastEval() })
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    const unknownTip = (groupId: number) => groupRow(groupId).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')
    const opened = async (groupId: number) => {
      document.querySelector<HTMLElement>(`[data-qp-expand="${groupId}"]`)!.click()
      await flushPromises()
      return document.querySelector(`[data-qp-group-detail="${groupId}"] [data-qp-line="ttfo"]`)!
    }
    expect(unknownTip(53)).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.noAction.none')
    expect(unknownTip(54)).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.identity.gateOffGroup')
    // Not judged in that group: the statistical causes are not why; the numbers stay.
    const notJudged = await opened(54)
    expect(notJudged.querySelector('.gdet__t')!.textContent).toBe('admin.schedQuality.identity.gateOffGroup')
    expect(notJudged.querySelector('[data-qp-evidence-item="n"]')).not.toBeNull()
    expect(notJudged.querySelector('[data-qp-evidence-why]')).toBeNull()

    closeSchedDrawer()
    await flushPromises()
    const off = { ...detail(), global_enabled: false }
    off.groups[0].by_model[0].lines.ttfo = line('ttfo', 'unknown', { evidence: ttfoEvidence({ causes: ['mass_insufficient'] }) })
    api.accountDetail.mockResolvedValue(off)
    await openOn(7, { model: 'gpt-a' })
    expect(unknownTip(53)).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.identity.gateOffGlobal')
    // Judging goes on with the switch off, so its causes still say why the line is unknown.
    const judged = await opened(53)
    expect(judged.querySelector('.gdet__t')!.textContent).toBe('admin.schedQuality.identity.gateOffGlobal')
    expect(judged.querySelector('[data-qp-evidence-why]')).not.toBeNull()
  })

  // Both switches off on a fresh view: the group is still not judged, so its words and evidence say that, not "judging
  // goes on".
  it('reads a group whose gate is off as not judged even with quality scheduling switched off too', async () => {
    const value = { ...detail(), global_enabled: false }
    value.groups[1].policy = { ...policy, enabled: false }
    value.groups[1].by_model[0].lines.ttfo = line('ttfo', 'unknown', { evidence: ttfoEvidence({ causes: ['mass_insufficient'] }) })
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    expect(groupRow(54).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')).toBe(
      'admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.identity.gateOffGroup'
    )
    expect(groupRow(53).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')).toBe(
      'admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.identity.gateOffGlobal'
    )
    document.querySelector<HTMLElement>('[data-qp-expand="54"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-qp-group-detail="54"] [data-qp-line="ttfo"] [data-qp-evidence-why]')).toBeNull()
  })

  // Before the engine's first cycle the backend answers stale with global_enabled false: that is not the switch.
  it('says the data is stale, not that quality scheduling is off, while the view is stale', async () => {
    const value = detail()
    value.groups[1].by_model = []
    // An assessment on record does not override a stale view either.
    value.groups[0].by_model[0].lines.cache = line('cache', 'unknown', { last_eval: lastEval() })
    api.accountDetail.mockResolvedValue({ ...value, stale: true, global_enabled: false, at: '' })
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    expect(groupRow(54).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')).toBe(
      'admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.tip.stale'
    )
    expect(groupRow(53).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')).toBe(
      'admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.tip.stale'
    )
    expect(document.querySelector('[data-sq-pause]')!.getAttribute('aria-label')).toBe('admin.schedQuality.tip.stale')
    expect(document.body.textContent).not.toContain('gateOffGlobal')
    expect(document.body.textContent).not.toContain('tip.globalOff')
  })

  it('opens a group row to each line\'s evidence, with why an unknown first output is unknown', async () => {
    const value = detail()
    value.groups[1].by_model[0].lines.ttfo = line('ttfo', 'unknown', { evidence: ttfoEvidence({ n: 4.25, s: 1, causes: ['mass_insufficient', 'no_peers'] }), source_groups: [53, 54] })
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    const toggle = document.querySelector<HTMLElement>('[data-qp-expand="54"]')!
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(document.querySelector('[data-qp-group-detail="54"]')).toBeNull()
    toggle.click()
    await flushPromises()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    const opened = document.querySelector<HTMLElement>('[data-qp-group-detail="54"]')!
    expect(Array.from(opened.querySelectorAll<HTMLElement>('[data-qp-line]'), (el) => el.dataset.qpLine)).toEqual(['ttfo', 'error', 'cache'])
    const ttfo = opened.querySelector('[data-qp-line="ttfo"]')!
    expect(ttfo.querySelector('.idw')!.textContent).toBe('admin.schedQuality.identity.unknown')
    expect(ttfo.querySelector('.gdet__t')!.textContent).toBe('admin.schedQuality.noAction.none')
    const item = (key: string) => ttfo.querySelector(`[data-qp-evidence-item="${key}"]`)!.textContent!.trim()
    expect(item('limit_s')).toBe('admin.schedQuality.evidence.ttfo.limit_s:{"v":"10"}')
    expect(item('n')).toBe('admin.schedQuality.evidence.ttfo.n:{"v":"4.3"}')
    expect(item('s')).toBe('admin.schedQuality.evidence.ttfo.s:{"v":"1"}')
    expect(ttfo.querySelector('[data-qp-evidence-why]')!.textContent!.trim()).toBe(
      'admin.schedQuality.evidence.why:{"reasons":"admin.schedQuality.evidence.causes.mass_insufficientadmin.schedQuality.evidence.reasonJoineradmin.schedQuality.evidence.causes.no_peers"}'
    )
    expect(ttfo.querySelector('.ev__src')!.textContent).toBe('admin.schedQuality.evidence.sources:{"names":"晨星组admin.schedQuality.evidence.joiner北辰组"}')
    // The other lines carry no unknown causes.
    expect(opened.querySelector('[data-qp-line="cache"] [data-qp-evidence-why]')).toBeNull()
    // Another group stays closed.
    expect(document.querySelector('[data-qp-group-detail="53"]')).toBeNull()
  })

  // Browser check #10: the heading's title and note ran together in English ("DecisionsOne record …").
  it('keeps the decisions title and its note apart', () => {
    const w = mount(SchedDecisions, { props: { accountId: 7, groupName: () => '晨星组' } })
    const heading = w.get('h3').text()
    expect(heading).toMatch(/admin\.schedQuality\.decisions\.title\s+admin\.schedQuality\.decisions\.note$/)
    expect(w.get('[data-qp-decisions-note]').classes()).toContain('qp-h-note')
  })

  // final-fe review item 5: jsdom applies no SFC styles, so the rule itself is checked: the note wraps, never cut.
  it('lets the decisions note wrap instead of cutting it on a narrow drawer', () => {
    const style = decisionsSource.slice(decisionsSource.indexOf('<style scoped>'))
    const note = style.slice(style.indexOf('.qp-h-note {'), style.indexOf('}', style.indexOf('.qp-h-note {')))
    const heading = style.slice(style.indexOf('.qp-h {'), style.indexOf('}', style.indexOf('.qp-h {')))
    expect(note).not.toMatch(/white-space:\s*nowrap|text-overflow:\s*ellipsis|overflow:\s*hidden/)
    expect(heading).toMatch(/flex-wrap:\s*wrap/)
  })

  // Round 4 (16-②): on a 320 or 390 drawer Evidence ran out of view, the row being one line nothing in it could narrow.
  // jsdom lays nothing out: the narrow rule and the row it lays out are checked here, the geometry in a browser.
  it('lays a decision row out in three columns on a narrow list, Evidence whole and still the same button', async () => {
    const style = decisionsSource.slice(decisionsSource.indexOf('<style scoped>'))
    const block = (css: string, selector: string) => {
      const start = css.indexOf(`${selector} {`)
      expect(start).toBeGreaterThanOrEqual(0)
      return css.slice(start, css.indexOf('}', start))
    }
    // The list's own width decides, not the viewport's.
    expect(block(style, '.dlist')).toMatch(/container:\s*sched-decisions\s*\/\s*inline-size/)
    // The widest list measured to need it (English, another day's trial with Evidence) is 454px.
    const at = style.indexOf('@container sched-decisions (max-width: 454px) {')
    expect(at).toBeGreaterThan(0)
    const narrow = style.slice(at)
    const main = block(narrow, '.drow__main')
    expect(main).toMatch(/display:\s*grid/)
    expect(main).toMatch(/grid-template-columns:\s*minmax\(0, 1fr\) max-content/)
    expect(main).toMatch(/grid-template-areas:\s*'what ev'\s*'where where'/)
    // The time has its own column; the action's line and the lines under it share the next one (design 6.5).
    expect(block(style, '.drow')).toMatch(/grid-template-columns:\s*auto minmax\(0, 1fr\)/)
    expect(block(narrow, '.drow__what')).toMatch(/grid-area:\s*what;\s*overflow-wrap:\s*anywhere/)
    expect(block(narrow, '.drow__where')).toMatch(/grid-area:\s*where;\s*overflow-wrap:\s*anywhere;\s*white-space:\s*normal/)
    expect(block(narrow, '.drow__ev')).toMatch(/grid-area:\s*ev/)
    // An opened evidence wraps anywhere at every width: a long source group name must not widen the list.
    expect(block(style, '.drow__basis')).toMatch(/min-width:\s*0;\s*overflow-wrap:\s*anywhere/)

    const record = (id: number, patch: Partial<BackendDecision> = {}): BackendDecision => ({
      id, at: inMinutes(-id), group_id: 53, model: 'gpt-b', line: 'ttfo', action: 'trial_timeout', level: 0, until: null, basis: basis('ttfo'), ...patch
    })
    api.accountDecisions.mockResolvedValueOnce([record(2), record(1, { line: '', action: 'relax', basis: null })])
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, groupName: () => '晨星组' } })
    await w.get('[data-qp-decisions-toggle]').trigger('click')
    await flushPromises()
    const cells = (selector: string) =>
      Array.from(w.get(selector).element.children, (cell) => [...cell.classList].find((name) => name.startsWith('drow__')))
    expect(cells('[data-qp-decision="2"]')).toEqual(['drow__time', 'drow__body'])
    expect(cells('[data-qp-decision="2"] .drow__body')).toEqual(['drow__main', 'drow__sub'])
    expect(cells('[data-qp-decision="2"] .drow__main')).toEqual(['drow__what', 'drow__where', 'drow__ev'])
    // A record of the previous rules has no basis and so no Evidence.
    expect(cells('[data-qp-decision="1"] .drow__main')).toEqual(['drow__what', 'drow__where'])
    const button = w.get<HTMLButtonElement>('[data-qp-decision-evidence="2"]').element
    expect([button.tagName, button.type, button.disabled, button.hasAttribute('tabindex')]).toEqual(['BUTTON', 'button', false, false])
    button.focus()
    expect(document.activeElement).toBe(button)
    button.click()
    await flushPromises()
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(w.find('[data-qp-decision="2"] [data-qp-evidence]').exists()).toBe(true)
  })

  // M1: a recovery while the list is collapsed: opening it again shows the new record, not the history loaded before.
  it('reloads a collapsed decision list on its next opening after a recovery or an exemption', async () => {
    const record = (id: number, action: string): BackendDecision =>
      ({ id, at: inMinutes(-id), group_id: 53, model: 'gpt-b', line: 'ttfo', action, level: 0, until: null })
    api.accountDecisions.mockResolvedValueOnce([record(1, 'enter')]).mockResolvedValueOnce([record(2, 'reset'), record(1, 'enter')])
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, groupName: () => '晨星组' } })
    const toggle = w.get('[data-qp-decisions-toggle]')
    await toggle.trigger('click')
    await flushPromises()
    await toggle.trigger('click')
    await markSchedChanged()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenCalledTimes(1)
    await toggle.trigger('click')
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenCalledTimes(2)
    expect(w.find('[data-qp-decision="2"]').exists()).toBe(true)
  })

  // A first-page refresh that fails drops the old list, so its retry asks for the first page (no before_id) again.
  it('retries the first page after a refresh fails, never paging on from the records shown before', async () => {
    const record = (id: number, action: string): BackendDecision =>
      ({ id, at: inMinutes(-id), group_id: 53, model: 'gpt-b', line: 'ttfo', action, level: 0, until: null })
    api.accountDecisions
      .mockResolvedValueOnce(Array.from({ length: 50 }, (_, i) => record(60 - i, 'enter')))
      .mockRejectedValueOnce(new Error('down'))
      .mockResolvedValueOnce([record(61, 'reset'), record(60, 'enter')])
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, groupName: () => '晨星组' } })
    await w.get('[data-qp-decisions-toggle]').trigger('click')
    await flushPromises()
    await markSchedChanged()
    await flushPromises()
    expect(w.find('[data-qp-decision="60"]').exists()).toBe(false)
    expect(w.find('[data-qp-decisions-more]').exists()).toBe(false)
    await w.get('[data-qp-decisions-failed] button').trigger('click')
    await flushPromises()
    expect(api.accountDecisions.mock.calls.map((call) => call[1])).toEqual([
      { limit: 50, beforeId: undefined }, { limit: 50, beforeId: undefined }, { limit: 50, beforeId: undefined }
    ])
    expect(w.find('[data-qp-decision="61"]').exists()).toBe(true)
  })

  it('loads the decision records on first opening, opens one to its evidence, and pages back from the last one', async () => {
    atLocalNoon()
    const record = (id: number, patch: Partial<BackendDecision> = {}): BackendDecision => ({
      id, at: inMinutes(-id), group_id: 53, model: 'gpt-b', line: 'ttfo', action: 'enter', level: 1, until: inMinutes(20),
      basis: { rule_version: '3.0', line: 'ttfo', kind: 'regular', threshold: 10, source_groups: [53], ttft: ttfoEvidence() },
      ...patch
    })
    const page = [record(60), record(59, { action: 'trial', until: inMinutes(50) }), record(58, { line: '', action: 'renew', basis: null })]
    api.accountDecisions.mockResolvedValueOnce([...page, ...Array.from({ length: 47 }, (_, i) => record(57 - i, { action: 'release', until: null }))])
      .mockResolvedValueOnce([record(3, { action: 'clear', until: null })])
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(api.accountDecisions).not.toHaveBeenCalled()
    document.querySelector<HTMLElement>('[data-qp-decisions-toggle]')!.click()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenCalledWith(7, { limit: 50, beforeId: undefined })
    const rowOf = (id: number) => document.querySelector<HTMLElement>(`[data-qp-decision="${id}"]`)!
    expect(rowOf(60).querySelector('.drow__what')!.textContent).toBe('admin.schedQuality.decisions.action.enter')
    expect(rowOf(60).querySelector('.drow__where')!.textContent).toBe('晨星组 · gpt-b · admin.schedQuality.dim.ttfo')
    expect(rowOf(60).querySelector('.drow__sub')!.textContent).toMatch(/^admin\.schedQuality\.identity\.rearUntil:\{"time":"\d\d:\d\d","minutes":40\}$/)
    expect(rowOf(59).querySelector('.drow__sub')!.textContent).toMatch(/^admin\.schedQuality\.decisions\.trialUntil:/)
    // A record of the previous rules: named as such with its action, no evidence to open.
    expect(rowOf(58).querySelector('.drow__what')!.textContent).toBe('admin.schedQuality.decisions.legacy:{"action":"admin.schedQuality.decisions.action.renew"}')
    expect(rowOf(58).querySelector('[data-qp-decision-evidence]')).toBeNull()

    expect(rowOf(60).querySelector('[data-qp-evidence]')).toBeNull()
    rowOf(60).querySelector<HTMLElement>('[data-qp-decision-evidence]')!.click()
    await flushPromises()
    expect(rowOf(60).querySelector('[data-qp-evidence-item="limit_s"]')!.textContent!.trim()).toBe('admin.schedQuality.evidence.ttfo.limit_s:{"v":"10"}')

    document.querySelector<HTMLElement>('[data-qp-decisions-more]')!.click()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenLastCalledWith(7, { limit: 50, beforeId: 11 })
    expect(rowOf(3).querySelector('.drow__what')!.textContent).toBe('admin.schedQuality.decisions.action.clear')
    expect(document.querySelector('[data-qp-decisions-more]')).toBeNull()

    // A recovery or an exemption from the panel writes records: the open list starts over from the newest.
    api.accountDecisions.mockResolvedValueOnce([record(61, { action: 'reset', until: null })])
    await markSchedChanged()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenLastCalledWith(7, { limit: 50, beforeId: undefined })
    expect(rowOf(61).querySelector('.drow__what')!.textContent).toBe('admin.schedQuality.decisions.action.reset')
    expect(document.querySelector('[data-qp-decision="60"]')).toBeNull()
  })

  const U = 'admin.schedQuality.evidence.samples.unknownValue'
  /** The counts line as the stub renders it: a known total carries its unit template, an unknown one is just unknown. */
  const counts = (dim: string, local: string, total: string | null) =>
    `admin.schedQuality.evidence.samples.${dim}:${JSON.stringify({ local, total: total === null ? U : `admin.schedQuality.evidence.samples.total.${dim}:${JSON.stringify({ n: total })}` })}`
  // Round 3 (plan item 9): each line says how much of its counted evidence is this group's; older records say unknown.
  it('shows this group\'s and all the counted units of each line, the first output\'s weight, and why an error line did not recover', async () => {
    const value = detail()
    value.groups[1].by_model[0].lines = {
      ttfo: line('ttfo', 'pass'),
      error: line('error', 'unknown', { evidence: errorEvidence({ recovery_over_limit: true }) }),
      cache: legacyLine('cache', 'unknown')
    }
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    document.querySelector<HTMLElement>('[data-qp-expand="54"]')!.click()
    await flushPromises()
    const lineOf = (dim: string) => document.querySelector<HTMLElement>(`[data-qp-group-detail="54"] [data-qp-line="${dim}"]`)!
    const text = (el: Element | null) => el?.textContent?.trim()
    expect(text(lineOf('ttfo').querySelector('[data-qp-evidence-samples]'))).toBe(counts('ttfo', '11', '30'))
    expect(text(lineOf('ttfo').querySelector('[data-qp-evidence-weight]'))).toBe('admin.schedQuality.evidence.samples.weight:{"local":"9.5","total":"24.5"}')
    expect(text(lineOf('error').querySelector('[data-qp-evidence-samples]'))).toBe(counts('error', '40', '80'))
    expect(text(lineOf('error').querySelector('[data-qp-evidence-over-limit]'))).toBe('admin.schedQuality.evidence.recoveryOverLimit')
    // A record from before the counts were kept: unknown with why, not worked out from the source groups.
    expect(text(lineOf('cache').querySelector('[data-qp-evidence-samples]'))).toBe(counts('cache', U, null))
    expect(lineOf('cache').querySelector('[data-qp-evidence-samples-why]')).not.toBeNull()
    expect(lineOf('ttfo').querySelector('[data-qp-evidence-samples-why]')).toBeNull()
    expect(lineOf('ttfo').querySelector('[data-qp-evidence-over-limit]')).toBeNull()
  })

  // Review item 6: each recorded figure is shown on its own; a missing one is unknown and hides nothing else.
  it('reads an older first-output record field by field: unknown counts and local weight, the recorded total weight', () => {
    const ttfo = legacyLine('ttfo', 'pass')
    const w = mount(SchedEvidence, { props: { dim: 'ttfo', evidence: ttfo.evidence, samples: ttfo, sourceGroups: [53] } })
    expect(w.get('[data-qp-evidence-samples]').text()).toBe(counts('ttfo', U, null))
    expect(w.get('[data-qp-evidence-weight]').text()).toBe(`admin.schedQuality.evidence.samples.weight:${JSON.stringify({ local: U, total: '24.5' })}`)
    expect(w.get('[data-qp-evidence-samples-why]').text()).toBe('admin.schedQuality.evidence.samples.unknownWhy')
    const error = legacyLine('error', 'unknown')
    const e = mount(SchedEvidence, { props: { dim: 'error', evidence: error.evidence, samples: error } })
    expect(e.find('[data-qp-evidence-over-limit]').exists()).toBe(false)
    expect(e.find('[data-qp-evidence-weight]').exists()).toBe(false)
  })

  // Second review item 5: one side missing never hides the other; a recorded 0 is 0, not unknown.
  it.each([
    ['this group 0, all missing', { samples_local: 0, samples_total: null }, counts('error', '0', null)],
    ['this group missing, all 12', { samples_local: null, samples_total: 12 }, counts('error', U, '12')]
  ])('shows each side of the counts on its own: %s', (_, samples, expected) => {
    const w = mount(SchedEvidence, { props: { dim: 'error', evidence: errorEvidence(), samples } })
    expect(w.get('[data-qp-evidence-samples]').text()).toBe(expected)
    expect(w.get('[data-qp-evidence-samples-why]').text()).toBe('admin.schedQuality.evidence.samples.unknownWhy')
  })

  it('shows a basis\'s own counts when its nested evidence is missing', () => {
    const w = mount(SchedEvidence, { props: { dim: 'ttfo', evidence: undefined, samples: { samples_local: 4, samples_total: 12 } } })
    expect(w.get('[data-qp-evidence-samples]').text()).toBe(counts('ttfo', '4', '12'))
    expect(w.text()).toContain('admin.schedQuality.evidence.none')
    expect(w.find('[data-qp-evidence-weight]').exists()).toBe(false)
    expect(w.find('[data-qp-evidence-samples-why]').exists()).toBe(false)
  })

  it('says why a trial ended without a verdict from the basis: no traffic, not enough evidence, the seats, or an older record', async () => {
    const record = (id: number, patch: Partial<BackendDecision>): BackendDecision =>
      ({ id, at: inMinutes(-id), group_id: 53, model: 'gpt-b', line: 'ttfo', action: 'trial_timeout', level: 0, until: null, ...patch })
    api.accountDecisions.mockResolvedValueOnce([
      record(6, { basis: basis('ttfo', { kind: 'trial', samples_local: 0, samples_total: 12, trial_seats: 'full' }) }),
      record(5, { basis: basis('ttfo', { kind: 'trial', samples_local: 6, ttft: ttfoEvidence({ causes: ['mass_insufficient', 'sessions_insufficient'] }), trial_seats: 'not_observed' }) }),
      record(4, { line: 'error', basis: basis('error', { kind: 'trial', samples_local: 6, trial_seats: 'unknown' }) }),
      record(3, { basis: legacyBasis('ttfo', { kind: 'trial' }) }),
      record(2, { basis: null }),
      record(8, { basis: legacyBasis('ttfo', { kind: 'trial', trial_seats: 'full' }) }),
      record(7, { basis: basis('ttfo', { kind: 'trial', samples_local: 0 }) }),
      record(1, { action: 'enter', level: 1, until: inMinutes(20), basis: basis('ttfo') })
    ])
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, groupName: () => '晨星组' } })
    await w.get('[data-qp-decisions-toggle]').trigger('click')
    await flushPromises()
    const reason = (id: number) => w.find(`[data-qp-trial-timeout="${id}"]`)
    const line = (reasons: string) => `admin.schedQuality.decisions.timeout.line:${JSON.stringify({ reasons })}`
    const J = 'admin.schedQuality.decisions.timeout.joiner'
    expect(reason(6).text()).toBe(line(`admin.schedQuality.decisions.timeout.noTraffic${J}admin.schedQuality.decisions.timeout.seats.full`))
    const causes = 'admin.schedQuality.evidence.causes.mass_insufficientadmin.schedQuality.evidence.joineradmin.schedQuality.evidence.causes.sessions_insufficient'
    expect(reason(5).text()).toBe(line(`admin.schedQuality.decisions.timeout.evidenceWhy:${JSON.stringify({ reasons: causes })}${J}admin.schedQuality.decisions.timeout.seats.not_observed`))
    expect(reason(4).text()).toBe(line(`admin.schedQuality.decisions.timeout.evidence${J}admin.schedQuality.decisions.timeout.seats.unknown`))
    expect(reason(3).text()).toBe(line('admin.schedQuality.decisions.timeout.legacy'))
    expect(reason(2).text()).toBe(line('admin.schedQuality.decisions.timeout.legacy'))
    expect(reason(1).exists()).toBe(false)
    // One part recorded, the other not: the recorded one stays, the other says it was not recorded.
    expect(reason(8).text()).toBe(line(`admin.schedQuality.decisions.timeout.trafficLegacy${J}admin.schedQuality.decisions.timeout.seats.full`))
    expect(reason(7).text()).toBe(line(`admin.schedQuality.decisions.timeout.noTraffic${J}admin.schedQuality.decisions.timeout.seatsLegacy`))
    // The basis opens to the same counts the line shows.
    await w.get('[data-qp-decision-evidence="5"]').trigger('click')
    expect(w.get('[data-qp-decision="5"] [data-qp-evidence-samples]').text()).toBe(counts('ttfo', '6', '30'))
    await w.get('[data-qp-decision-evidence="3"]').trigger('click')
    expect(w.get('[data-qp-decision="3"] [data-qp-evidence-samples]').text()).toBe(counts('ttfo', U, null))
    expect(w.get('[data-qp-decision="3"] [data-qp-evidence-weight]').text()).toBe(`admin.schedQuality.evidence.samples.weight:${JSON.stringify({ local: U, total: '24.5' })}`)
  })
})

describe('SchedQualityPanel 12 hours and why not judged (round 4)', () => {
  const perfOf = (id: number) => {
    const row = (groupId: number, model: string, patch: Parameters<typeof perfRow>[1] = {}) => perfRow(id, { group_id: groupId, model, ...patch }, { n: 463, slowRate: 0.531 })
    return perfAnswer(
      [row(53, 'gpt-b'), row(53, 'gpt-a', { requests: 120 }), row(99, 'gpt-b', { member: false, requests: 30 })],
      [perfGroup({ group_id: 53, name: '晨星组' }), perfGroup({ group_id: 54, name: '北辰组' }), perfGroup({ group_id: 99, name: '旧组' })]
    )
  }
  const perfRows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-qp-perf-row]'), (row) => row.dataset.qpPerfRow)

  it('lists the account in each group for the model tab, a group without traffic in grey, then a group it left', async () => {
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(api.accountPerformance).toHaveBeenCalledWith(7, expect.anything())
    expect(perfRows()).toEqual(['53:7:gpt-b', '54:none', '99:7:gpt-b'])
    expect(document.querySelector('[data-qp-perf-row="53:7:gpt-b"] [data-qp-perf-cell="requests"]')!.textContent).toBe('463')
    // A group the account left: marked beside the name (which a phone cuts), not inside it, and only once.
    const leftRow = document.querySelector<HTMLElement>('[data-qp-perf-row="99:7:gpt-b"]')!
    expect(leftRow.querySelector('[data-qp-perf-left]')!.textContent).toBe('admin.schedQuality.perf.left')
    expect(leftRow.querySelector('.prow__name')!.textContent).not.toContain('notMember')
    expect(leftRow.textContent!.match(/admin\.schedQuality\.perf\.(left|notMember)/g)).toEqual(['admin.schedQuality.perf.left'])
    expect(leftRow.getAttribute('title')).toContain('admin.schedQuality.perf.notMember')
    tab('gpt-a').click()
    await flushPromises()
    expect(perfRows()).toEqual(['53:7:gpt-a', '54:none'])
    document.querySelector<HTMLElement>('[data-qp-perf-expand="53:7:gpt-a"]')!.click()
    await flushPromises()
    // The panel has one account's rows: no rank there.
    const score = document.querySelector('[data-qp-perf-detail="53:7:gpt-a"] [data-sq-perf-detail-item="score"]')!.textContent!
    expect(score).not.toContain('scoreRank')
  })

  it('offers a retry when the 12 hours did not load, without failing the rest of the panel', async () => {
    api.accountPerformance.mockRejectedValue(new Error('down'))
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(document.querySelector('[data-qp-perf-failed]')).not.toBeNull()
    expect(groupRow(53)).not.toBeNull()
    api.accountPerformance.mockResolvedValue(perfOf(7))
    document.querySelector<HTMLElement>('[data-qp-perf-failed] button')!.click()
    await flushPromises()
    expect(perfRows()).toContain('53:7:gpt-a')
  })

  // Review item 3: the main reason of the latest assessment, acted or not; none on record says so (not 证据不够).
  it('gives an unknown line the main reason of its latest assessment, acted or not, and says when there is none', async () => {
    const value = detail()
    const row = value.groups[0].by_model[1]
    row.lines.error = line('error', 'unknown', { last_eval: lastEval({ acted: true, action: 'trial_timeout', reason: 'peer_short', have: 8, need: 20 }) })
    row.lines.cache = line('cache', 'unknown', { last_eval: null })
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    const tips = Array.from(groupRow(53).querySelectorAll<HTMLElement>('[data-qp-mark]'), (mark) => mark.getAttribute('aria-label'))
    expect(tips[1]).toMatch(/^admin\.schedQuality\.dim\.error · admin\.schedQuality\.identity\.unknown · admin\.schedQuality\.noAction\.long\.peer_short:.*"have\\":\\"8\\"/)
    expect(tips[2]).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.noAction.none')
  })

  it('writes each line\'s latest assessment in the detail as its result and its main reason', async () => {
    const perf = perfOf(7)
    perf.rows[0].lines.ttfo = { identity: 'unknown', until: null, last_eval: lastEval({ acted: true, action: 'trial_timeout', reason: 'peer_short' }) }
    perf.rows[0].lines.error = { identity: 'pass', until: null, last_eval: lastEval({ reason: 'within', have: null, need: null }) }
    api.accountPerformance.mockResolvedValue(perf)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    document.querySelector<HTMLElement>('[data-qp-perf-expand="53:7:gpt-b"]')!.click()
    await flushPromises()
    const lines = Array.from(document.querySelectorAll('[data-qp-perf-detail="53:7:gpt-b"] [data-sq-perf-detail-item="verdict"] .pdet__line'), (el) => el.textContent!)
    expect(lines[0]).toMatch(/^admin\.schedQuality\.perf\.detail\.evalLine:.*"result":"admin\.schedQuality\.decisions\.action\.trial_timeout","reason":"admin\.schedQuality\.noAction\.long\.peer_short/)
    expect(lines[1]).toMatch(/^admin\.schedQuality\.perf\.detail\.evalLine:.*"result":"admin\.schedQuality\.perf\.detail\.resultNone:\{\}","reason":"admin\.schedQuality\.noAction\.long\.within/)
    expect(lines[2]).toBe('admin.schedQuality.perf.detail.evalNone:{"line":"admin.schedQuality.lineName.cache"}')
  })

  // 裁决 6, review item 9: "各分组" from the account's detail; the 12-hour detail from its own row only (the detail's
  // counts may be another assessment's).
  it('says a rear line\'s evidence all came from other groups, each place from its own counts', async () => {
    const value = detail()
    Object.assign(value.groups[0].by_model[1].lines.ttfo, { samples_local: 0, samples_total: 14 })
    api.accountDetail.mockResolvedValue(value)
    const perf = perfOf(7)
    perf.rows[0].lines.ttfo = { identity: 'rear', until: null, last_eval: lastEval({ acted: true, action: 'enter', reason: 'over' }), samples_local: null, samples_total: null }
    api.accountPerformance.mockResolvedValue(perf)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    const tip = groupRow(53).querySelector<HTMLElement>('[data-qp-mark="ttfo"]')!.getAttribute('aria-label')!
    expect(tip).toContain('admin.schedQuality.perf.then:')
    expect(tip).toContain('admin.schedQuality.perf.elsewhere')
    document.querySelector<HTMLElement>('[data-qp-perf-expand="53:7:gpt-b"]')!.click()
    await flushPromises()
    const verdict = () => document.querySelector('[data-qp-perf-detail="53:7:gpt-b"] [data-sq-perf-detail-item="verdict"]')!.textContent!
    expect(verdict()).not.toContain('admin.schedQuality.perf.detail.elsewhere')
    // The row's own counts say it.
    const own = structuredClone(perf)
    Object.assign(own.rows[0].lines.ttfo, { samples_local: 0, samples_total: 14 })
    api.accountPerformance.mockResolvedValue(own)
    // A rear line with samples of its own in the detail says nothing of the kind in "各分组".
    Object.assign(value.groups[0].by_model[1].lines.ttfo, { samples_local: 3, samples_total: 14 })
    api.accountDetail.mockResolvedValue(structuredClone(value))
    await markSchedChanged()
    await flushPromises()
    expect(verdict()).toContain('admin.schedQuality.perf.detail.elsewhere')
    expect(groupRow(53).querySelector<HTMLElement>('[data-qp-mark="ttfo"]')!.getAttribute('aria-label')).not.toContain('elsewhere')
  })

  // Review items 5 and 6: still loading is not "no requests"; another model's requests keep the rows; a group with the
  // gate off is not counted.
  it('says the statistics are loading, keeps the group rows for a model without requests, and marks a group not counted', async () => {
    api.accountPerformance.mockResolvedValue(perfAnswer([], [], { complete: false, window_from: '2026-11-19T12:10:00Z' }))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-loading-stats]')).not.toBeNull()
    expect(document.querySelector('[data-qp-perf-empty]')).toBeNull()
    expect(perfRows()).toEqual([])

    closeSchedDrawer()
    await flushPromises()
    const value = detail()
    value.groups[1].policy = { ...policy, enabled: false }
    api.accountDetail.mockResolvedValue(value)
    const onlyA = perfOf(7)
    onlyA.rows = onlyA.rows.filter((row) => row.model === 'gpt-a')
    // A full snapshot lists every group the account is in: one it does not list was not counted in it.
    onlyA.groups = onlyA.groups.filter((group) => group.group_id !== 54)
    api.accountPerformance.mockResolvedValue(onlyA)
    await openOn(7, { model: 'gpt-b' })
    expect(perfRows()).toEqual(['53:none', '54:none'])
    expect(document.querySelector('[data-qp-perf-empty="model"]')!.textContent).toBe('admin.schedQuality.perf.panel.emptyModel:{"model":"gpt-b"}')
    // Acceptance round 2: a counted group without requests says so in one short note, not five dashes; each row carries
    // its group's id for a phone, where names are cut alike.
    expect(document.querySelector('[data-qp-perf-row="53:none"] [data-qp-perf-no-requests]')!.textContent).toBe('admin.schedQuality.perf.panel.noRequests')
    expect(document.querySelector('[data-qp-perf-row="54:none"] [data-qp-perf-no-requests]')).toBeNull()
    expect(document.querySelector('[data-qp-perf-row="53:none"] .prow__gid')!.textContent).toBe('#53')
    // A short note in the cell; why (the gate off when the snapshot was made, or the account not in the group yet) in its tip.
    const off = document.querySelector<HTMLElement>('[data-qp-perf-row="54:none"] [data-qp-perf-gate-off]')!
    expect(off.textContent).toBe('admin.schedQuality.perf.panel.gateOff')
    expect((off as unknown as { __tipText: string }).__tipText).toBe('admin.schedQuality.perf.panel.gateOffTip')
    expect(off.getAttribute('tabindex')).toBe('0')
    expect(document.querySelector('[data-qp-perf-row="53:none"] [data-qp-perf-gate-off]')).toBeNull()
  })

  // Review item 7: the 12-hour snapshot's own staleness, apart from the account's detail and from a failed refresh.
  it('marks the section stale from the 12-hour answer itself', async () => {
    api.accountPerformance.mockResolvedValue({ ...perfOf(7), stale: true })
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-section="groups"] [data-sq-pause]')).toBeNull()
    expect(document.querySelector('[data-qp-section="perf"] [data-sq-pause]')!.getAttribute('aria-label')).toBe('admin.schedQuality.tip.stale')
    expect(document.querySelector('[data-qp-perf-refresh-failed]')).toBeNull()
  })

  // Opus item 2 (M10), review item 10: asked at once on opening (the page's first load is long over), and only once.
  it('asks for the 12 hours at once on opening the panel, once', async () => {
    mount(Panel, { attachTo: document.body })
    await flushPromises()
    expect(api.accountPerformance).not.toHaveBeenCalled()
    await openOn(7, { model: 'gpt-b' })
    expect(api.accountPerformance).toHaveBeenCalledTimes(1)
  })

  // Opus item 2 (M9): another account's rows never show under the new one while its answer is on its way.
  it('shows no rows of the previous account while the new one\'s 12 hours are on their way', async () => {
    const next = deferred<ReturnType<typeof perfOf>>()
    api.accountPerformance.mockImplementation((id: number) => (id === 7 ? Promise.resolve(perfOf(7)) : next.promise))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(perfRows()).toContain('53:7:gpt-b')
    await openOn(8, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-loading]')).not.toBeNull()
    expect(perfRows()).toEqual([])
    next.resolve(perfOf(8))
    await flushPromises()
    expect(perfRows()).toContain('53:8:gpt-b')
  })

  // Re-review item 2: a group's setting as counted in the snapshot first; groups not counted keep their row; "no
  // requests" only in the counted groups; nothing at all says so instead of a bare header.
  it('keeps groups not counted, takes the snapshot\'s own gate first, and scopes "no requests" to the counted groups', async () => {
    // The detail has both gates on now; the snapshot counted 北辰组 with its gate off.
    api.accountPerformance.mockResolvedValue(perfAnswer([], [perfGroup({ group_id: 53 }), perfGroup({ group_id: 54, gate_enabled: false })]))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(perfRows()).toEqual(['53:none', '54:none'])
    expect(document.querySelector('[data-qp-perf-row="54:none"] [data-qp-perf-gate-off]')).not.toBeNull()
    expect(document.querySelector('[data-qp-perf-row="53:none"] [data-qp-perf-no-requests]')!.textContent).toBe('admin.schedQuality.perf.panel.noRequests')
    expect(document.querySelector('[data-qp-perf-empty="account"]')!.textContent).toBe('admin.schedQuality.perf.panel.empty')

    // The gate switched off after the snapshot counted the group: it was counted, so it is no "not counted" row.
    closeSchedDrawer()
    await flushPromises()
    const counted = detail()
    counted.groups[0].policy = { ...policy, enabled: false }
    api.accountDetail.mockResolvedValue(counted)
    const snapshot = perfOf(7)
    snapshot.rows = snapshot.rows.filter((row) => row.model === 'gpt-a')
    api.accountPerformance.mockResolvedValue(snapshot)
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-row="53:none"] [data-qp-perf-gate-off]')).toBeNull()

    // No group now and no row in the window: one sentence, not a bare header.
    closeSchedDrawer()
    await flushPromises()
    api.accountDetail.mockResolvedValue({ ...detail(), groups: [] })
    api.accountPerformance.mockResolvedValue(perfAnswer([]))
    await openOn(7)
    expect(document.querySelector('[data-qp-perf-none]')).not.toBeNull()
    expect(document.querySelector('[data-qp-section="perf"] [role="table"]')).toBeNull()
  })

  // Re-review item 3: a partly covered window keeps its coverage and draws no "no requests" conclusion.
  it('keeps the coverage and draws no "no requests" conclusion while the window is partly covered', async () => {
    const partial = perfOf(7)
    partial.rows = partial.rows.filter((row) => row.model === 'gpt-a')
    Object.assign(partial, { complete: false, window_from: '2026-11-19T08:40:00Z' })
    api.accountPerformance.mockResolvedValue(partial)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-note]')!.textContent).toContain('admin.schedQuality.perf.windowPartial:{"hours":"3.5"')
    expect(document.querySelector('[data-qp-perf-empty]')!.getAttribute('data-qp-perf-empty')).toBe('partial')
    expect(document.querySelector('[data-qp-perf-empty]')!.textContent).toBe('admin.schedQuality.perf.panel.partialModel:{"hours":"3.5","model":"gpt-b"}')
  })

  // Re-review item 4: another account asks at once, and the request still on its way for the previous one is cancelled.
  it('asks for another account at once, cancelling the previous account\'s request on its way', async () => {
    const signals: AbortSignal[] = []
    api.accountPerformance.mockImplementation((id: number, options: { signal: AbortSignal }) => {
      signals.push(options.signal)
      return id === 7 ? new Promise(() => {}) : Promise.resolve(perfOf(id))
    })
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    await openOn(8, { model: 'gpt-b' })
    expect(api.accountPerformance.mock.calls.map((call) => call[0])).toEqual([7, 8])
    expect(signals[0].aborted).toBe(true)
    expect(perfRows()).toContain('53:8:gpt-b')
  })

  // fe-polish review (Astra 1): the account's detail comes every 5 s, the 12-hour snapshot every minute. A group the
  // newer detail has and the full snapshot does not list was not counted in it: never "no requests" from the detail's
  // own gate. A snapshot still loading the window lists only some groups: there the detail's gate stands in, as before.
  it('says a group the full snapshot does not list was not counted, never "no requests", when the detail is newer', async () => {
    vi.useFakeTimers()
    onTestFinished(() => {
      vi.useRealTimers()
    })
    const snapshot = perfOf(7)
    snapshot.rows = snapshot.rows.filter((row) => row.model === 'gpt-a')
    api.accountPerformance.mockResolvedValue(snapshot)
    mount(Panel, { attachTo: document.body })
    openSchedDrawer({ id: 7, name: 'acc-7' }, { model: 'gpt-b' })
    await vi.advanceTimersByTimeAsync(0)
    expect(perfRows()).toEqual(['53:none', '54:none'])

    const newer = detail()
    newer.groups.push({ group_id: 98, name: '新组', policy, exemption: null, by_model: [model('gpt-b')] })
    api.accountDetail.mockResolvedValue(newer)
    await vi.advanceTimersByTimeAsync(5000)
    // The detail came again, the snapshot did not.
    expect(api.accountPerformance).toHaveBeenCalledTimes(1)
    expect(perfRows()).toEqual(['53:none', '54:none', '98:none'])
    const added = document.querySelector('[data-qp-perf-row="98:none"]')!
    expect(added.querySelector('[data-qp-perf-gate-off]')!.textContent).toBe('admin.schedQuality.perf.panel.gateOff')
    expect(added.querySelector('[data-qp-perf-no-requests]')).toBeNull()
    expect(document.querySelector('[data-qp-perf-row="53:none"] [data-qp-perf-no-requests]')!.textContent).toBe('admin.schedQuality.perf.panel.noRequests')

    closeSchedDrawer()
    await vi.advanceTimersByTimeAsync(0)
    api.accountPerformance.mockResolvedValue({ ...snapshot, complete: false, window_from: '2026-11-19T08:40:00Z' })
    openSchedDrawer({ id: 7, name: 'acc-7' }, { model: 'gpt-b' })
    await vi.advanceTimersByTimeAsync(0)
    expect(document.querySelector('[data-qp-perf-row="98:none"] [data-qp-perf-no-requests]')!.textContent).toBe('admin.schedQuality.perf.panel.noFiguresYet')
    expect(document.querySelector('[data-qp-perf-row="98:none"] [data-qp-perf-gate-off]')).toBeNull()
  })

  // fe-polish review (Astra 3 and its re-check 1–2, Opus 4). jsdom lays nothing out: the rules are checked here, the text
  // boxes in a browser (第四轮/scratch/perf-fe/rowcheck.mjs). Under 440px the group takes a line of its own across the row
  // (at 320px the id ran over the requests) and every figure goes under its own heading, placed by hand (left to the
  // grid, excess, cache and switches slid one heading left). From 440px "left the group" takes a line of its own in the
  // group's cell: beside a name cut to nothing it ran over the requests.
  it('puts every figure under its own heading on a phone, and "left the group" clear of the figures at any width', async () => {
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    const heads = Array.from(document.querySelectorAll<HTMLElement>('[data-qp-section="perf"] [data-qp-perf-col]'), (el) => el.dataset.qpPerfCol!)
    expect(heads).toEqual(['group', 'requests', 'slow', 'excess', 'cache', 'switches'])
    const cells = Array.from(document.querySelectorAll<HTMLElement>('[data-qp-perf-row="53:7:gpt-b"] [data-qp-perf-cell]'), (el) => el.dataset.qpPerfCell)
    expect(cells).toEqual(heads.slice(1))

    const style = panelPerfSource.slice(panelPerfSource.indexOf('<style scoped>'))
    // The rule whose whole selector is `selector` (".prow__group", not "… > .prow__group").
    const block = (css: string, selector: string) => {
      const found = new RegExp(`\\n\\s*${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} \\{`).exec(css)
      expect(found).not.toBeNull()
      return css.slice(found!.index, css.indexOf('}', found!.index))
    }
    const at = style.indexOf('@media (max-width: 439px) {')
    expect(at).toBeGreaterThan(0)
    const wide = style.slice(0, at)
    const narrow = style.slice(at)
    // A phone: the group on a line of its own, the figures on the next, the slow share gone, each figure in the column of
    // its heading among those shown.
    expect(block(narrow, '.prow:not(.prow--head) > .prow__group')).toMatch(/grid-column:\s*1 \/ -1/)
    expect(block(narrow, '.prow:not(.prow--head) > :not(.prow__group)')).toMatch(/grid-row:\s*2/)
    expect(block(narrow, '.prow__slow')).toMatch(/display:\s*none/)
    const shown = heads.filter((key) => key !== 'slow')
    shown.forEach((key, index) => {
      if (key !== 'group') expect(narrow).toMatch(new RegExp(`\\[data-qp-perf-cell='${key}'\\][^{]*\\{\\s*grid-column(-start)?:\\s*${index + 1};`))
    })
    // The id and "left the group" beside the name there, never shrinking: the name gives way.
    expect(block(narrow, '.prow__gid')).toMatch(/display:\s*inline/)
    expect(block(wide, '.prow__gid')).toMatch(/flex:\s*none/)
    expect(block(narrow, '.prow__left')).toMatch(/flex:\s*none/)
    expect(block(narrow, '.prow__group')).toMatch(/flex-wrap:\s*nowrap/)
    // From 440px: "left the group" on a line of its own in the group's cell; the name, its basis 0, never wraps the
    // first line and is cut there.
    expect(block(wide, '.prow__group')).toMatch(/flex-wrap:\s*wrap/)
    expect(block(wide, '.prow__left')).toMatch(/flex:\s*0 0 100%/)
    expect(block(wide, '.prow__left')).not.toMatch(/nowrap/)
    expect(block(wide, '.prow__name')).toMatch(/flex:\s*1 1 0;/)
    expect(block(wide, '.prow__name')).toMatch(/min-width:\s*0/)
  })

  // Acceptance round 2: a counted group without requests while the window is still filling says "no figures yet".
  it('says no figures yet for a group without requests while the window is partly covered', async () => {
    const partial = perfOf(7)
    partial.rows = partial.rows.filter((row) => row.model === 'gpt-a')
    Object.assign(partial, { complete: false, window_from: '2026-11-19T08:40:00Z' })
    api.accountPerformance.mockResolvedValue(partial)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-row="53:none"] [data-qp-perf-no-requests]')!.textContent).toBe('admin.schedQuality.perf.panel.noFiguresYet')
  })
})
