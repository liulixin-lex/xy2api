import { afterAll, afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, withDirectives } from 'vue'

import type { BackendAccountDetail, BackendDecision, BackendGroupModel, BackendPolicy } from '@/api/admin/schedQualityTypes'
import { basis, errorEvidence, groupModel, legacyBasis, legacyLine, line, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import { perfAnswer, perfGroup, perfRow } from '@/__tests__/fixtures/schedQuality/performance'
import SchedQualityPanel from '../SchedQualityPanel.vue'
import SchedDecisions from '../SchedDecisions.vue'
import SchedEvidence from '../SchedEvidence.vue'
import { closeSchedBoard, closeSchedDrawer, markSchedChanged, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'
import type { LiveTable } from '@/composables/useLiveTable'
import { withSchedHost } from './schedLayerHost'
import { vTip } from '@/utils/tip'

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
    expect(groupRow(53).querySelector('[data-test="qp-state"]')!.textContent).toContain('admin.schedQuality.panel.placeUntil.rear')
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

  // The dot carries a v-tip: keyboard focus on it may scroll the page only when Tab brought it there, never when the
  // closing panel gives it back (preventScroll), so the table stays where the user scrolled it.
  it('gives focus back to an off-screen dot without scrolling the page, and its tip still shows', async () => {
    mount(Panel, { attachTo: document.body })
    const dot = mount(defineComponent({
      setup: () => () => withDirectives(h('button', { type: 'button', 'data-quality-opener': '7:53' }, 'dot'), [[vTip, 'dot tip']])
    }), { attachTo: document.body }).element as HTMLElement
    vi.spyOn(dot, 'getBoundingClientRect').mockReturnValue({ top: -400, bottom: -380, left: 100, right: 124, width: 24, height: 20 } as DOMRect)
    dot.scrollIntoView = vi.fn()
    await openOn(7, { groupId: 53 })
    tab('gpt-a').focus()
    // The tip's two frames, driven here rather than left to jsdom's frame clock.
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] })
    onTestFinished(() => {
      vi.useRealTimers()
    })
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    await flushPromises()
    expect([panel(), document.activeElement]).toEqual([null, dot])
    vi.advanceTimersToNextFrame()
    vi.advanceTimersToNextFrame()
    expect(dot.scrollIntoView).not.toHaveBeenCalled()
    expect(dot.getAttribute('aria-describedby')).toBe('sq-tip')
  })

  it('lists every model\'s own recheck time in the automatic states, as a time of day', async () => {
    atLocalNoon()
    mount(Panel, { attachTo: document.body })
    await openOn()
    const times = ['iq:gpt-a', 'iq:gpt-b'].map((key) => document.querySelector(`[data-qp-auto="${key}"] .arow__t`)!.textContent)
    expect(times).toEqual(['admin.schedQuality.panel.autoRecheck:{"time":"12:05"}', 'admin.schedQuality.panel.autoRecheck:{"time":"12:42"}'])
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

  const U = 'admin.schedQuality.evidence.samples.unknownValue'
  /** The counts line as the stub renders it: a known total carries its unit template, an unknown one is just unknown. */
  const counts = (dim: string, local: string, total: string | null) =>
    `admin.schedQuality.evidence.samples.${dim}:${JSON.stringify({ local, total: total === null ? U : `admin.schedQuality.evidence.samples.total.${dim}:${JSON.stringify({ n: total })}` })}`
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
    await flushPromises()
    // Eight entries: the newest six first, then the rest.
    await w.get('[data-qp-decisions-more]').trigger('click')
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

describe('SchedQualityPanel groups table with the last 12 hours', () => {
  /** gpt-b in 晨星组: 463 first sends, 53.1% slow; 210 turns at 90% cache; 480 attempts, 12 failed. */
  const perfOf = (id: number) => perfAnswer(
    [
      perfRow(id, { group_id: 53, model: 'gpt-b', attempts: { failures: 12 } }, { n: 463, slowRate: 0.531 }),
      perfRow(id, { group_id: 53, model: 'gpt-a', requests: 120 }),
      perfRow(id, { group_id: 99, model: 'gpt-b', member: false, requests: 30 })
    ],
    [perfGroup({ group_id: 53, name: '晨星组' }), perfGroup({ group_id: 54, name: '北辰组' }), perfGroup({ group_id: 99, name: '旧组' })]
  )
  const cells = (groupId: number) => Array.from(groupRow(groupId).querySelectorAll<HTMLElement>('[data-qp-cell]'), (cell) => [cell.dataset.qpCell, cell.textContent, cell.className.replace(/^gt__n gt__n--\w+ /, '')])
  const place = (groupId: number) => groupRow(groupId).querySelector('[data-test="qp-state"]')!.textContent!.trim()
  const whys = (groupId: number) => Array.from(groupRow(groupId).querySelectorAll<HTMLElement>('[data-qp-why]'), (el) => [el.dataset.qpWhy, el.textContent!.trim()])

  it('puts each group\'s place and its 12-hour figures in one row, each share in its line\'s verdict colour', async () => {
    atLocalNoon()
    const value = detail()
    value.groups[0].by_model[1].lines.error = line('error', 'pass')
    api.accountDetail.mockResolvedValue(value)
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    // Rear until the end of its rear line (a clock time, not a countdown).
    expect(place(53)).toMatch(/^admin\.schedQuality\.panel\.placeUntil\.rear:\{"time":"12:20"\}$/)
    expect(cells(53)).toEqual([
      ['requests', '463', 'is-plain'],
      ['ttfo', '53.1%', 'is-rear'],
      ['error', '2.5%', 'is-pass'],
      ['cache', '90.0%', 'is-unknown']
    ])
    expect(groupRow(53).querySelector('[data-qp-cell="ttfo"]')!.getAttribute('aria-label')).toBe('admin.schedQuality.panel.col.ttfo 53.1% admin.schedQuality.identity.rear')
    // A group without traffic of the model: front, a grey dash for every figure; a group the account left is not listed.
    expect(place(54)).toBe('admin.schedQuality.placement.front')
    expect(cells(54).map(([, text, tone]) => `${text} ${tone}`)).toEqual(['— is-none', '— is-none', '— is-none', '— is-none'])
    expect(document.querySelector('[data-qp-group-row="99"]')).toBeNull()
    // The whole group name, no cut; the heading tells the window and when it was worked out.
    expect(groupRow(53).querySelector('.gt__title')!.textContent).toBe('晨星组')
    expect(document.querySelector('[data-qp-perf-note]')!.textContent!.trim()).toMatch(/^admin\.schedQuality\.panel\.window · admin\.schedQuality\.perf\.updated:/)

    tab('gpt-a').click()
    await flushPromises()
    expect(cells(53).map(([col, text]) => `${col} ${text}`)).toEqual(['requests 120', 'ttfo 20.1%', 'error 0.0%', 'cache 90.0%'])
    expect(place(53)).toBe('admin.schedQuality.placement.front')
  })

  it('says in one line per line why a group is at the rear or on trial, with the figures that judgement was taken on', async () => {
    atLocalNoon()
    const value = detail()
    const row = value.groups[0].by_model[1]
    Object.assign(row.lines.ttfo, { samples_local: 0, samples_total: 14 })
    row.lines.error = line('error', 'rear', { level: 0, until: inMinutes(35), evidence: errorEvidence({ limit: 0.05, padded: 80, failed: 6 }) })
    row.lines.cache = line('cache', 'trial', { trial_until: inMinutes(50) })
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    const J = 'admin.schedQuality.panel.why.joiner'
    const ttfo = ttfoEvidence()
    expect(whys(53)).toEqual([
      ['ttfo', `admin.schedQuality.dim.ttfo${['admin.schedQuality.panel.why.ttfo:' + JSON.stringify({ limit: String(ttfo.limit_s), share: `${((ttfo.k / ttfo.n) * 100).toFixed(1)}%`, peer: `${(ttfo.prior_mu * 100).toFixed(1)}%` }), 'admin.schedQuality.panel.why.elsewhere', 'admin.schedQuality.panel.why.recheck:{"time":"12:20"}'].join(J)}`],
      ['error', `admin.schedQuality.dim.error${['admin.schedQuality.panel.why.error:{"n":"80","failed":"6","share":"7.5%","limit":"5%"}', 'admin.schedQuality.panel.why.recheck:{"time":"12:35"}'].join(J)}`],
      ['cache', 'admin.schedQuality.dim.cacheadmin.schedQuality.panel.why.trial:{"time":"12:50"}']
    ])
    // The place ends with its latest rear line.
    expect(place(53)).toBe('admin.schedQuality.panel.placeUntil.rear:{"time":"12:35"}')
    // A group at the front says nothing more.
    expect(whys(54)).toEqual([])
  })

  it('reads an exemption, a group with its gate off and a stale view in the place, and keeps the row actions', async () => {
    atLocalNoon()
    const value = detail()
    value.groups[0].exemption = { group_id: 53, expires_at: inMinutes(90), reason: '' }
    value.groups[1].policy = { ...policy, enabled: false }
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(place(53)).toBe('admin.schedQuality.panel.placeUntil.exempt:{"time":"13:30"}')
    expect(whys(53)).toEqual([])
    expect(groupRow(53).querySelector('[data-qp-trigger="unexempt:53"]')!.getAttribute('aria-label')).toBe('admin.schedQuality.action.undoExempt 晨星组')
    const off = groupRow(54).querySelector<HTMLElement>('[data-qp-gate-off]')!
    expect(off.textContent).toBe('admin.schedQuality.panel.gateOff')
    expect((off as unknown as { __tipText: string }).__tipText).toBe('admin.schedQuality.identity.gateOffGroup')
    expect(groupRow(54).querySelector('[data-qp-trigger="exempt:54"]')).not.toBeNull()

    closeSchedDrawer()
    await flushPromises()
    api.accountDetail.mockResolvedValue({ ...detail(), stale: true, global_enabled: false, at: '' })
    await openOn(7, { model: 'gpt-b' })
    // Before the engine's first cycle the backend answers stale with global_enabled false: that is not the switch.
    expect(document.querySelector('[data-qp-section="groups"] [data-sq-pause]')!.getAttribute('aria-label')).toBe('admin.schedQuality.tip.stale')
    expect(place(53)).toBe('admin.schedQuality.placement.front')
  })

  it('shows every model tab by its whole name and marks the models with a group at the rear', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    expect(Array.from(document.querySelectorAll<HTMLElement>('[data-qp-tab]'), (el) => el.textContent!.trim())).toEqual([
      'gpt-a', 'gpt-badmin.schedQuality.panel.tabBack'
    ])
    expect(document.querySelector('[data-qp-section="recent"]')).toBeNull()
    // No automatic state, no section.
    api.accountDetail.mockResolvedValue({ ...detail(), auto_states: [] })
    await markSchedChanged()
    await flushPromises()
    expect(document.querySelector('[data-qp-section="auto"]')).toBeNull()
  })

  it('offers a retry when the 12 hours did not load, without failing the rest of the panel', async () => {
    api.accountPerformance.mockRejectedValue(new Error('down'))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-failed]')).not.toBeNull()
    expect(place(53)).toMatch(/placeUntil\.rear/)
    api.accountPerformance.mockResolvedValue(perfOf(7))
    document.querySelector<HTMLElement>('[data-qp-perf-note] button')!.click()
    await flushPromises()
    expect(groupRow(53).querySelector('[data-qp-cell="requests"]')!.textContent).toBe('463')
  })

  // The account's detail comes every 5 s, the 12-hour snapshot every minute: asked at once on opening, once; another
  // account's figures never show under the new one while its answer is on its way, and its request is cancelled.
  it('asks for the 12 hours at once on opening, and never shows the previous account\'s figures under the next', async () => {
    const signals: AbortSignal[] = []
    const next = deferred<ReturnType<typeof perfOf>>()
    api.accountPerformance.mockImplementation((id: number, options: { signal: AbortSignal }) => {
      signals.push(options.signal)
      return id === 7 ? Promise.resolve(perfOf(7)) : next.promise
    })
    mount(Panel, { attachTo: document.body })
    await flushPromises()
    expect(api.accountPerformance).not.toHaveBeenCalled()
    await openOn(7, { model: 'gpt-b' })
    expect(api.accountPerformance).toHaveBeenCalledTimes(1)
    expect(groupRow(53).querySelector('[data-qp-cell="requests"]')!.textContent).toBe('463')
    await openOn(8, { model: 'gpt-b' })
    expect(groupRow(53).querySelector('[data-qp-cell="requests"]')!.textContent).toBe('—')
    expect(document.querySelector('[data-qp-perf-note]')!.textContent!.trim()).toBe('admin.schedQuality.panel.windowLoading')
    const updated = perfOf(8)
    updated.rows[0].requests = 77
    next.resolve(updated)
    await flushPromises()
    expect(groupRow(53).querySelector('[data-qp-cell="requests"]')!.textContent).toBe('77')
    expect(api.accountPerformance.mock.calls.map((call) => call[0])).toEqual([7, 8])
  })

  it('says how much of the window a partly covered snapshot covers', async () => {
    api.accountPerformance.mockResolvedValue(perfAnswer([], [perfGroup({ group_id: 53 })], { complete: false, at: '2026-11-19T12:10:00Z', window_from: '2026-11-19T09:10:00Z' }))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-note]')!.textContent!.trim()).toMatch(/^admin\.schedQuality\.panel\.windowPartial:\{"hours":"3\.0"\} · /)
  })
})

describe('SchedQualityPanel decision records', () => {
  const record = (id: number, patch: Partial<BackendDecision> = {}): BackendDecision => ({
    id, at: inMinutes(-id * 2), group_id: 53, model: 'gpt-b', line: 'ttfo', action: 'enter', level: 1, until: inMinutes(20), basis: basis('ttfo'), ...patch
  })
  const entries = () => Array.from(document.querySelectorAll<HTMLElement>('[data-qp-decision]'), (el) => el.dataset.qpDecision)

  it('loads with the panel, merges one moment\'s records across groups, shows the newest six, then the rest and older pages', async () => {
    atLocalNoon()
    const at = inMinutes(-1)
    const page = [
      record(90, { at, group_id: 53 }), record(89, { at, group_id: 54 }), record(88, { at, group_id: 53 }),
      record(87, { action: 'trial', until: inMinutes(50), at: inMinutes(-30) }),
      record(86, { line: '', action: 'renew', basis: null, at: inMinutes(-40) }),
      ...Array.from({ length: 47 }, (_, i) => record(85 - i, { action: 'release', until: null }))
    ]
    api.accountDecisions.mockResolvedValueOnce(page).mockResolvedValueOnce([record(3, { action: 'clear', until: null, at: inMinutes(-500) })])
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(api.accountDecisions).toHaveBeenCalledWith(7, { limit: 50, beforeId: undefined })
    // The same minute, action, model and line in two groups (and a repeat for one of them) read as one entry.
    expect(entries()).toEqual(['90', '87', '86', '85', '84', '83'])
    const entry = (id: number) => document.querySelector<HTMLElement>(`[data-qp-decision="${id}"]`)!
    expect(entry(90).querySelector('.drow__where')!.textContent).toBe('gpt-b · admin.schedQuality.dim.ttfo · 晨星组admin.schedQuality.evidence.joiner北辰组')
    expect(entry(90).querySelector('.drow__end')!.textContent).toBe('admin.schedQuality.decisions.rearUntil:{"time":"12:20","minutes":40}')
    expect(entry(87).querySelector('.drow__end')!.textContent).toBe('admin.schedQuality.decisions.trialUntil:{"time":"12:50"}')
    // A record of the previous rules: named as such, no evidence to open.
    expect(entry(86).querySelector('.drow__what')!.textContent).toBe('admin.schedQuality.decisions.legacy:{"action":"admin.schedQuality.decisions.action.renew"}')
    expect(entry(86).querySelector('[data-qp-decision-evidence]')).toBeNull()
    // The merged entry opens to each group's evidence, under its group's name.
    entry(90).querySelector<HTMLElement>('[data-qp-decision-evidence]')!.click()
    await flushPromises()
    expect(Array.from(entry(90).querySelectorAll('.drow__group'), (el) => el.textContent)).toEqual(['晨星组', '北辰组'])
    expect(entry(90).querySelectorAll('[data-qp-evidence]')).toHaveLength(2)

    const more = () => document.querySelector<HTMLElement>('[data-qp-decisions-more]')
    more()!.click()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenCalledTimes(1)
    expect(entries()).toHaveLength(50)
    more()!.click()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenLastCalledWith(7, { limit: 50, beforeId: 39 })
    expect(entries().at(-1)).toBe('3')
    expect(more()).toBeNull()

    // A recovery or an exemption from the panel writes records: the list starts over from the newest six.
    api.accountDecisions.mockResolvedValueOnce([record(91, { action: 'reset', until: null }), ...page])
    await markSchedChanged()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenLastCalledWith(7, { limit: 50, beforeId: undefined })
    expect(entries()).toEqual(['91', '90', '87', '86', '85', '84'])
  })

  // A first-page refresh that fails drops the old list, so its retry asks for the first page (no before_id) again.
  it('retries the first page after a refresh fails, never paging on from the records shown before', async () => {
    api.accountDecisions
      .mockResolvedValueOnce(Array.from({ length: 50 }, (_, i) => record(60 - i)))
      .mockRejectedValueOnce(new Error('down'))
      .mockResolvedValueOnce([record(61, { action: 'reset', until: null }), record(60)])
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, groupName: () => '晨星组' } })
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
})
