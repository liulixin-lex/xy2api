import { afterAll, afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { BackendAccountDetail, BackendDecision, BackendGroupModel, BackendPolicy } from '@/api/admin/schedQualityTypes'
import { groupModel, line, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import SchedQualityPanel from '../SchedQualityPanel.vue'
import SchedDecisions from '../SchedDecisions.vue'
import { closeSchedBoard, closeSchedDrawer, markSchedChanged, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import type { LiveTable } from '@/composables/useLiveTable'
import { withSchedHost } from './schedLayerHost'

const { api, backendSummary } = vi.hoisted(() => ({
  api: { accountDetail: vi.fn(), accountDecisions: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn(), reset: vi.fn() },
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
    expect(tips[2]).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.identity.unknownWhy')
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
  it('says the gate is off instead of 证据不够: in one group (not judged), or switched off for everything (judged as usual)', async () => {
    const value = detail()
    value.groups[1].policy = { ...policy, enabled: false }
    value.groups[1].by_model[0].lines.ttfo = line('ttfo', 'unknown', { evidence: ttfoEvidence({ causes: ['mass_insufficient'] }) })
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    const unknownTip = (groupId: number) => groupRow(groupId).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')
    const opened = async (groupId: number) => {
      document.querySelector<HTMLElement>(`[data-qp-expand="${groupId}"]`)!.click()
      await flushPromises()
      return document.querySelector(`[data-qp-group-detail="${groupId}"] [data-qp-line="ttfo"]`)!
    }
    expect(unknownTip(53)).toBe('admin.schedQuality.dim.cache · admin.schedQuality.identity.unknown · admin.schedQuality.identity.unknownWhy')
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
    api.accountDetail.mockResolvedValue({ ...value, stale: true, global_enabled: false, at: '' })
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    expect(groupRow(54).querySelector('[data-qp-mark="cache"]')!.getAttribute('aria-label')).toBe(
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
    expect(ttfo.querySelector('.gdet__t')!.textContent).toBe('admin.schedQuality.identity.unknownWhy')
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
})
