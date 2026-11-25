import { afterAll, afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, withDirectives } from 'vue'

import type { BackendAccountDetail, BackendDecision, BackendGroupModel, BackendPolicy } from '@/api/admin/schedQualityTypes'
import { basis, cacheEvidence, groupModel, legacyBasis, line, ttfoEvidence } from '@/__tests__/fixtures/schedQuality/lines'
import { lastEval, perfAnswer, perfGroup, perfRow } from '@/__tests__/fixtures/schedQuality/performance'
import SchedQualityPanel from '../SchedQualityPanel.vue'
import SchedDecisions from '../SchedDecisions.vue'
import AccountGroupPills from '@/components/admin/account/table/AccountGroupPills.vue'
import type { AdminGroup } from '@/types'
import type { TipCard } from '@/utils/tip'
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
    exemptions: [],
    current_rule_version: '4.0'
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
    expect(panel()!.querySelector('[data-qp-section="groups"]')).not.toBeNull()
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
    expect(panel()!.querySelector('[data-qp-section="groups"]')).not.toBeNull()
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
    expect(panel()!.querySelector('[data-qp-section="groups"]')).not.toBeNull()
  })

  it('closes the exemption popover when the panel switches to another account (7h-2)', async () => {
    mount(Panel, { attachTo: document.body })
    await openOn(7)
    groupRow(54).querySelector<HTMLElement>('[data-qp-place-trigger="54"]')!.click()
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
    expect(tab('gpt-b').firstElementChild!.getAttribute('data-qp-tab-mark')).toBe('rear')
    expect(tab('gpt-a').querySelector('[data-qp-tab-mark]')).toBeNull()

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
    expect(panel()!.querySelector('.qp-actions')).toBeNull()
    expect(panel()!.querySelector('[data-qp-loading]')).toBeNull()
    const body = panel()!.querySelector<HTMLElement>('[data-qp-body]')!
    expect(body.classList.contains('opacity-60')).toBe(true)
    expect(body.hasAttribute('inert')).toBe(true)
    expect(tab('gpt-b').getAttribute('aria-selected')).toBe('true')

    next.resolve({ ...detail(8), groups: [{ ...detail(8).groups[1] }] })
    await flushPromises()
    expect(body.classList.contains('opacity-60')).toBe(false)
    expect(body.hasAttribute('inert')).toBe(false)
    expect(panel()!.querySelector('[data-qp-section="groups"]')).not.toBeNull()
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
    groupRow(54).querySelector<HTMLElement>('[data-qp-place-trigger="54"]')!.click()
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
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, ruleVersion: '4.0', group: () => ({ name: '晨星组' }) } })
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
    // The basis opens to its figures: the counted units, all and this group's; an older record without counts has none.
    const samples = (id: number) => w.find(`[data-qp-decision="${id}"] [data-qp-evidence-samples]`)
    expect(samples(5).findAll('dd').map((cell) => cell.text())).toEqual(['30', '6'])
    expect(samples(3).exists()).toBe(false)
    expect(w.find('[data-qp-decision="3"] [data-qp-evidence-measure]').exists()).toBe(true)
  })
})

describe('SchedQualityPanel groups table with the last 12 hours', () => {
  /** gpt-b in 晨星组: 463 first sends, 53.1% slow against 19.7% for the group's others; 210 turns at 90% cache; 480 attempts, 12 failed. */
  const perfOf = (id: number) => perfAnswer(
    [
      perfRow(id, { group_id: 53, model: 'gpt-b', attempts: { failures: 12 } }, { n: 463, slowRate: 0.531 }),
      perfRow(id, { group_id: 53, model: 'gpt-a', requests: 120 }),
      perfRow(id, { group_id: 54, model: 'gpt-b', requests: 50 }),
      perfRow(id, { group_id: 99, model: 'gpt-b', member: false, requests: 30 })
    ],
    [perfGroup({ group_id: 53, name: '晨星组' }), perfGroup({ group_id: 54, name: '北辰组' }), perfGroup({ group_id: 99, name: '旧组' })]
  )
  const cells = (groupId: number) => Array.from(groupRow(groupId).querySelectorAll<HTMLElement>('[data-qp-cell]'), (cell) => [
    cell.dataset.qpCell, cell.firstElementChild!.textContent, [...cell.classList].find((name) => name.startsWith('is-')), cell.querySelector('[data-qp-ref]')?.textContent ?? null
  ])
  /** The line under a held figure: its text and its colour. */
  const judged = (groupId: number, col: string) => {
    const el = groupRow(groupId).querySelector<HTMLElement>(`[data-qp-cell="${col}"] [data-qp-judged]`)
    return el ? [el.textContent, [...el.classList].find((name) => name.startsWith('is-'))] : null
  }
  const dot = (groupId: number) => [...groupRow(groupId).querySelector('.status-dot')!.classList].find((name) => name.startsWith('status-dot--'))
  const place = (groupId: number) => groupRow(groupId).querySelector('[data-test="qp-state"] .gt__place-main')!.textContent!.trim()
  const stay = (groupId: number) => groupRow(groupId).querySelector('[data-qp-stay]')?.textContent ?? null
  const trigger = (groupId: number) => groupRow(groupId).querySelector<HTMLElement>(`[data-qp-place-trigger="${groupId}"]`)

  // Plan item 20: a held line's 12-hour figure is plain and its colour moves to the line under it — at the rear what
  // put it there against its line, on trial its latest assessment; under any other first-output share, the peers'.
  it('puts each group\'s place and 12-hour figures in one row; a held line shows what it was judged on under its figure', async () => {
    atLocalNoon()
    const value = detail()
    value.groups[0].by_model[1].lines.ttfo.basis = basis('ttfo', { ttft: ttfoEvidence({ n: 24, k: 9, tolerance: 0.17 }) })
    value.groups[0].by_model[1].lines.error = line('error', 'trial', {
      trial_started_at: inMinutes(-70), trial_until: inMinutes(50), last_eval: lastEval({ reason: 'trial_open', causes: ['clear_insufficient'], have: 12, need: 40 })
    })
    api.accountDetail.mockResolvedValue(value)
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    // At the rear: under it when the rear line is reviewed, a clock time, and no step (its level 1 is not shown); one
    // line holds it there, so it is not named (its amber figure shows which).
    expect(place(53)).toBe('admin.schedQuality.placement.rear')
    expect(stay(53)).toBe('admin.schedQuality.stay.rear:{"time":"12:20"}')
    expect(trigger(53)!.getAttribute('aria-label')).toBe('晨星组 admin.schedQuality.placement.rear admin.schedQuality.stay.rear:{"time":"12:20"}')
    expect(stay(54)).toBeNull()
    expect(cells(53)).toEqual([
      ['requests', '463', 'is-plain', null],
      ['ttfo', '53.1%', 'is-plain', null],
      ['error', '2.5%', 'is-plain', null],
      ['cache', '90.0%', 'is-unknown', null]
    ])
    // The rear line reads against the tolerance its decision was taken with; the share is never with its seconds.
    expect(judged(53, 'ttfo')).toEqual(['37.5 / 17', 'is-rear'])
    expect(judged(53, 'error')).toEqual(['admin.schedQuality.panel.lacks:{"have":12,"need":40}', 'is-trial'])
    expect(judged(53, 'cache')).toBeNull()
    expect(groupRow(53).querySelector('[data-qp-cell="ttfo"]')!.getAttribute('aria-label')).toBe(
      'admin.schedQuality.panel.col.ttfo 53.1% admin.schedQuality.identity.rear 37.5 / 17'
    )
    // An unheld first-output share has the peers' share under it (北辰组's row of gpt-b has no line state: unknown).
    expect(cells(54)[1]).toEqual(['ttfo', '20.1%', 'is-unknown', 'admin.schedQuality.panel.ref.ttfo:{"v":"19.7%"}'])
    expect(judged(54, 'ttfo')).toBeNull()
    // No sentence, no legend, no section title: the figures and the place say it.
    expect(document.querySelector('[data-qp-why], [data-qp-legend]')).toBeNull()
    expect(document.querySelector('[data-qp-perf-note]')!.textContent).toBe('admin.schedQuality.panel.window')
    expect((document.querySelector('[data-qp-perf-note]') as unknown as { __tipText: string }).__tipText).toMatch(/^admin\.schedQuality\.perf\.updated:/)
    // A group the account left is not listed.
    expect(document.querySelector('[data-qp-group-row="99"]')).toBeNull()
    expect(groupRow(53).querySelector('.gt__title')!.textContent).toBe('晨星组')

    tab('gpt-a').click()
    await flushPromises()
    expect(cells(53).map(([col, text]) => `${col} ${text}`)).toEqual(['requests 120', 'ttfo 20.1%', 'error 0.0%', 'cache 90.0%'])
    expect(place(53)).toBe('admin.schedQuality.placement.front')
  })

  // Plan item 20: a front with a passing line has a green dot; one none of whose lines passes, a grey one.
  it('greys the front dot when none of the lines passes', async () => {
    const value = detail()
    value.groups[1].by_model = [groupModel('gpt-a', { ttfo: 'unknown', error: 'unknown', cache: 'unknown' })]
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    expect([place(53), dot(53)]).toEqual(['admin.schedQuality.placement.front', 'status-dot--ok'])
    expect([place(54), dot(54)]).toEqual(['admin.schedQuality.placement.front', 'status-dot--observe'])
    // A group without a row for the model has no data: grey too.
    tab('gpt-b').click()
    await flushPromises()
    expect([place(54), dot(54)]).toEqual(['admin.schedQuality.placement.front', 'status-dot--observe'])
  })

  // Plan item 20: the 12-hour figures start at a recovery within them (the snapshot drops what came before it).
  it('heads the figures with the recovery time when the account was recovered within their window', async () => {
    const recovered = (reset: string | null) => {
      const value = detail()
      value.reset_at = reset
      return value
    }
    const heading = () => document.querySelector('[data-qp-perf-note]')!.textContent
    const clock = (iso: string) => {
      const date = new Date(iso)
      return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
    }
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    // Window 00:10–12:10 UTC: a recovery at 09:07 inside it heads the figures; one before it or after the figures were
    // worked out does not (they are the whole window, or from before the recovery).
    for (const [reset, expected] of [
      ['2026-11-19T09:07:00Z', `admin.schedQuality.panel.windowSince:{"time":"${clock('2026-11-19T09:07:00Z')}"}`],
      ['2026-11-19T00:05:00Z', 'admin.schedQuality.panel.window'],
      ['2026-11-19T12:15:00Z', 'admin.schedQuality.panel.window'],
      [null, 'admin.schedQuality.panel.window']
    ] as const) {
      api.accountDetail.mockResolvedValue(recovered(reset))
      closeSchedDrawer()
      await flushPromises()
      await openOn(7, { model: 'gpt-b' })
      expect(heading()).toBe(expected)
    }
  })

  // Astra 1: a group whose gate is now off keeps no figures of an older snapshot that still has its row.
  it('shows a group with its gate off in grey dashes, whatever the snapshot still holds for it', async () => {
    const value = detail()
    value.groups[1].policy = { ...policy, enabled: false }
    api.accountDetail.mockResolvedValue(value)
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(cells(54)).toEqual([
      ['requests', '—', 'is-none', null], ['ttfo', '—', 'is-none', null], ['error', '—', 'is-none', null], ['cache', '—', 'is-none', null]
    ])
    const off = groupRow(54).querySelector<HTMLElement>('[data-qp-gate-off]')!
    expect(off.textContent!.trim()).toBe('admin.schedQuality.panel.gateOff')
    expect((off as unknown as { __tipText: string }).__tipText).toBe('admin.schedQuality.identity.gateOffGroup')
    expect(trigger(54)).toBeNull()
    expect(cells(53)[0][1]).toBe('463')
  })

  it('shows a trial by its start and latest end, of the line that ends last, named when several are on trial', async () => {
    atLocalNoon()
    const value = detail()
    const [, b] = value.groups[0].by_model
    // An old 60-minute trial that started last but ends first, and a new 120-minute one.
    b.placement = 'trial'
    b.lines.ttfo = line('ttfo', 'trial', { trial_started_at: inMinutes(-10), trial_until: inMinutes(50) })
    b.lines.error = line('error', 'trial', { trial_started_at: inMinutes(-40), trial_until: inMinutes(80) })
    // 北辰组: one line on trial; the first-output line just passed its trial: back to normal, it reads unknown until
    // new evidence says pass, whatever the passed trial's verdict.
    value.groups[1].by_model = [groupModel('gpt-b', { ttfo: 'unknown', error: 'unknown', cache: 'trial' })]
    Object.assign(value.groups[1].by_model[0].lines.ttfo, { verdict: 'pass' })
    Object.assign(value.groups[1].by_model[0].lines.cache, { trial_started_at: inMinutes(-5), trial_until: inMinutes(115) })
    api.accountDetail.mockResolvedValue(value)
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(place(53)).toBe('admin.schedQuality.placement.trial')
    expect(stay(53)).toBe('admin.schedQuality.stay.line:{"line":"admin.schedQuality.panel.col.error","stay":"admin.schedQuality.stay.trial:{\\"start\\":\\"11:20\\",\\"end\\":\\"13:20\\"}"}')
    expect(stay(54)).toBe('admin.schedQuality.stay.trial:{"start":"11:55","end":"13:55"}')
    expect(cells(54).slice(1).map(([col, , tone]) => `${col} ${tone}`)).toEqual(['ttfo is-unknown', 'error is-unknown', 'cache is-plain'])
    // On trial the line under the figure is its latest assessment: before any, a dash in the trial's colour.
    expect(judged(54, 'cache')).toEqual(['—', 'is-trial'])
  })

  // Plan item 20: a trial whose latest assessment passes reads 达标 in green; never the reading it went on trial with.
  it('reads a trial line\'s latest assessment under its figure, green when it passes', async () => {
    const value = detail()
    const [, b] = value.groups[0].by_model
    b.placement = 'trial'
    b.lines.ttfo = line('ttfo', 'trial', { trial_until: inMinutes(50), basis: basis('ttfo', { ttft: ttfoEvidence({ n: 24, k: 9, tolerance: 0.2 }) }), last_eval: lastEval({ reason: 'within', causes: [], have: null, need: null }) })
    b.lines.cache = line('cache', 'trial', { trial_until: inMinutes(40), last_eval: lastEval({ reason: 'trial_open', causes: ['turns_insufficient'], have: 39, need: 40 }) })
    api.accountDetail.mockResolvedValue(value)
    api.accountPerformance.mockImplementation(async (id: number) => perfOf(id))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    expect(judged(53, 'ttfo')).toEqual(['admin.schedQuality.verdict.pass', 'is-pass'])
    expect(judged(53, 'cache')).toEqual(['admin.schedQuality.panel.lacks:{"have":39,"need":40}', 'is-trial'])
  })

  it('opens a group\'s exemption at its place, and only the removal of one in force there', async () => {
    atLocalNoon()
    const value = detail()
    value.groups[1].exemption = { group_id: 54, expires_at: inMinutes(90), reason: '' }
    api.accountDetail.mockResolvedValue(value)
    api.clearOverride.mockReset().mockResolvedValue(undefined)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(place(54)).toBe('admin.schedQuality.panel.placeUntil.exempt:{"time":"13:30"}')
    // A new exemption from a group's place: that group in scope, the place marked open.
    trigger(53)!.click()
    await flushPromises()
    expect(document.querySelector('[data-sq-popover] h3')!.textContent).toBe('admin.schedQuality.action.exempt')
    expect(trigger(53)!.getAttribute('aria-expanded')).toBe('true')
    expect(document.querySelector('[data-sq-popover] [data-sq-popover-undo]')).toBeNull()
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-secondary')!.click()
    await flushPromises()
    // An exempt group's place offers only to remove the exemption in force.
    trigger(54)!.click()
    await flushPromises()
    expect(document.querySelector('[data-sq-popover] h3')!.textContent).toBe('admin.schedQuality.panel.placeUntil.exempt:{"time":"13:30"}')
    expect(document.querySelector('[data-sq-popover] [role="radiogroup"]')).toBeNull()
    const undo = document.querySelector<HTMLElement>('[data-sq-popover-undo]')!
    expect(document.activeElement).toBe(undo)
    undo.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(7, 54)
    expect(api.setOverride).not.toHaveBeenCalled()
    expect(document.querySelector('[data-sq-popover]')).toBeNull()
  })

  it('removes the all-groups exemption from any group\'s place, saying so in its title', async () => {
    const value = detail()
    value.groups.forEach((group) => { group.exemption = { group_id: 0, expires_at: null, reason: '' } })
    value.exemptions = [{ group_id: 0, expires_at: null, reason: '' }]
    api.accountDetail.mockResolvedValue(value)
    api.clearOverride.mockReset().mockResolvedValue(undefined)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(place(53)).toBe('admin.schedQuality.panel.exemptForever')
    trigger(53)!.click()
    await flushPromises()
    expect(document.querySelector('[data-sq-popover] h3')!.textContent).toBe('admin.schedQuality.panel.exemptAll:{"what":"admin.schedQuality.panel.exemptForever"}')
    document.querySelector<HTMLElement>('[data-sq-popover-undo]')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(7, 0)
  })

  it('marks each model\'s tab with its worst place, and keeps a stale view apart from the switch', async () => {
    const value = detail()
    value.groups[1].by_model.push(groupModel('gpt-c', { cache: 'trial' }))
    api.accountDetail.mockResolvedValue(value)
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-a' })
    expect(Array.from(document.querySelectorAll<HTMLElement>('[data-qp-tab]'), (el) => [el.dataset.qpTab, el.querySelector<HTMLElement>('[data-qp-tab-mark]')?.dataset.qpTabMark ?? null])).toEqual([
      ['gpt-a', null], ['gpt-b', 'rear'], ['gpt-c', 'trial']
    ])
    expect(document.querySelector('[data-qp-section="recent"]')).toBeNull()

    closeSchedDrawer()
    await flushPromises()
    api.accountDetail.mockResolvedValue({ ...detail(), stale: true, global_enabled: false, at: '' })
    await openOn(7, { model: 'gpt-b' })
    // Before the engine's first cycle the backend answers stale with global_enabled false: that is not the switch.
    expect(document.querySelector('[data-qp-section="groups"] [data-sq-pause]')!.getAttribute('aria-label')).toBe('admin.schedQuality.tip.stale')
    expect(place(53)).toBe('admin.schedQuality.placement.front')
  })

  it('offers a retry when the 12 hours did not load, without failing the rest of the panel', async () => {
    api.accountPerformance.mockRejectedValue(new Error('down'))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(place(53)).toBe('admin.schedQuality.placement.rear')
    api.accountPerformance.mockResolvedValue(perfOf(7))
    document.querySelector<HTMLElement>('[data-qp-perf-retry]')!.click()
    await flushPromises()
    expect(groupRow(53).querySelector('[data-qp-cell="requests"]')!.textContent!.trim()).toBe('463')
    expect(document.querySelector('[data-qp-perf-retry]')).toBeNull()
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
    const requests = () => groupRow(53).querySelector('[data-qp-cell="requests"]')!.textContent!.trim()
    expect(requests()).toBe('463')
    await openOn(8, { model: 'gpt-b' })
    expect(requests()).toBe('—')
    const updated = perfOf(8)
    updated.rows[0].requests = 77
    next.resolve(updated)
    await flushPromises()
    expect(requests()).toBe('77')
    expect(api.accountPerformance.mock.calls.map((call) => call[0])).toEqual([7, 8])
  })

  it('heads the table with the hours a filling window covers', async () => {
    api.accountPerformance.mockResolvedValue(perfAnswer([], [perfGroup({ group_id: 53 })], { complete: false, at: '2026-11-19T12:10:00Z', window_from: '2026-11-19T09:10:00Z' }))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { model: 'gpt-b' })
    expect(document.querySelector('[data-qp-perf-note]')!.textContent).toBe('admin.schedQuality.panel.windowPartial:{"hours":"3.0"}')
  })
})

describe('SchedQualityPanel decision records', () => {
  const record = (id: number, patch: Partial<BackendDecision> = {}): BackendDecision => ({
    id, at: inMinutes(-id * 2), group_id: 53, model: 'gpt-b', line: 'ttfo', action: 'enter', level: 1, until: inMinutes(20), basis: basis('ttfo'), ...patch
  })
  const entries = () => Array.from(document.querySelectorAll<HTMLElement>('[data-qp-decision]'), (el) => el.dataset.qpDecision)
  /** The decision that put the line at the rear, on that basis. */
  const record90 = (dim: 'ttfo' | 'cache', value: BackendDecision['basis']): BackendDecision =>
    ({ id: 90, at: inMinutes(-1), group_id: 53, model: 'gpt-b', line: dim, action: 'enter', level: 0, until: inMinutes(20), basis: value })
  const entry = (id: number) => document.querySelector<HTMLElement>(`[data-qp-decision="${id}"]`)!

  it('loads with the panel: one line a record, merged across groups, newest six, then the rest and older pages', async () => {
    atLocalNoon()
    const at = inMinutes(-1)
    const page = [
      record(90, { at, group_id: 53 }), record(89, { at, group_id: 54 }),
      record(87, { action: 'trial', until: inMinutes(50), at: inMinutes(-30) }),
      record(86, { line: '', action: 'renew', basis: null, at: inMinutes(-40) }),
      ...Array.from({ length: 46 }, (_, i) => record(85 - i, { action: 'release', until: null }))
    ]
    api.accountDecisions.mockResolvedValueOnce(page).mockResolvedValueOnce([record(3, { action: 'clear', until: null, at: inMinutes(-500) })])
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(api.accountDecisions).toHaveBeenCalledWith(7, { limit: 50, beforeId: undefined })
    expect(entries()).toEqual(['90', '87', '86', '85', '84', '83'])
    const line = entry(90).querySelector<HTMLElement>('.drow__line')!
    expect(Array.from(line.children, (el) => [...el.classList].find((name) => name.startsWith('drow__')) ?? 'icon')).toEqual([
      'drow__time', 'drow__what', 'drow__end', 'drow__dim', 'drow__meta', 'drow__caret'
    ])
    expect(line.querySelector('.drow__end')!.textContent!.trim()).toBe('admin.schedQuality.decisions.until12:20')
    expect(line.querySelector('.drow__model')!.textContent).toBe('gpt-b')
    expect(line.querySelector('.drow__groups')!.getAttribute('title')).toBe('晨星组admin.schedQuality.evidence.joiner北辰组')
    expect(entry(87).querySelector('.drow__end')!.textContent!.trim()).toBe('admin.schedQuality.decisions.until12:50')
    // A record of the previous rules: named as such, nothing to open, so not a button.
    expect(entry(86).querySelector('.drow__what')!.textContent).toBe('admin.schedQuality.decisions.legacy:{"action":"admin.schedQuality.decisions.action.renew"}')
    expect(entry(86).querySelector('button')).toBeNull()
    // The whole line opens each group's evidence; closed, it is out of reach.
    const open = entry(90).querySelector<HTMLElement>('.drow__open')!
    expect(open.hasAttribute('inert')).toBe(true)
    line.click()
    await flushPromises()
    expect(line.getAttribute('aria-expanded')).toBe('true')
    expect(open.hasAttribute('inert')).toBe(false)
    // The two groups' evidence is alike: it shows once, without naming the groups again.
    expect(open.querySelectorAll('[data-qp-evidence]')).toHaveLength(1)
    expect(open.querySelector('.drow__group')).toBeNull()
    // What it read against its line: this fixture's record has no tolerance kept, so the line is unknown, not made up.
    expect(Array.from(open.querySelectorAll('[data-qp-evidence-measure] dd'), (el) => el.textContent)).toEqual(['4.9 / —'])

    const more = () => document.querySelector<HTMLElement>('[data-qp-decisions-more]')
    more()!.click()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenCalledTimes(1)
    expect(entries()).toHaveLength(49)
    more()!.click()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenLastCalledWith(7, { limit: 50, beforeId: 40 })
    expect(entries().at(-1)).toBe('3')
    expect(more()).toBeNull()

    // A recovery or an exemption from the panel writes records: the list starts over from the newest six.
    api.accountDecisions.mockResolvedValueOnce([record(91, { action: 'reset', until: null }), ...page])
    await markSchedChanged()
    await flushPromises()
    expect(api.accountDecisions).toHaveBeenLastCalledWith(7, { limit: 50, beforeId: undefined })
    expect(entries()).toEqual(['91', '90', '87', '86', '85', '84'])
  })

  // Plan item 18: records of another rule version than 4.0 (or none) are the previous rules'; under 4.0 a trial's end
  // reads by why it ended. Around the switch one minute may hold both versions, or two ends: each is its own line.
  it('titles each record by its rule version and, under 4.0, a trial\'s end by its release reason; mixed records stay apart', async () => {
    atLocalNoon()
    const at = inMinutes(-1)
    const old = (patch: Parameters<typeof basis>[1] = {}) => basis('ttfo', { rule_version: '3.1', ...patch })
    const ended = (reason: string) => basis('ttfo', { kind: 'trial', release_reason: reason, samples_local: 0, trial_seats: 'full' })
    api.accountDecisions.mockResolvedValueOnce([
      record(90, { at, group_id: 53 }),
      record(89, { at, group_id: 54, basis: old() }),
      record(80, { at: inMinutes(-10), group_id: 53, action: 'trial_pass', until: null, basis: ended('good') }),
      record(79, { at: inMinutes(-10), group_id: 54, action: 'trial_timeout', until: null, basis: ended('deadline') }),
      record(70, { at: inMinutes(-20), action: 'trial_fail', until: inMinutes(40), basis: ended('fail') }),
      record(60, { at: inMinutes(-30), action: 'trial_timeout', until: null, basis: old({ kind: 'trial', release_reason: 'deadline', samples_local: 0, trial_seats: 'full' }) }),
      record(50, { at: inMinutes(-40), action: 'clear', until: null, basis: old({ kind: 'clock' }) }),
      record(40, { at: inMinutes(-50), action: 'renew', basis: null })
    ])
    mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, ruleVersion: '4.0', group: (id: number) => ({ name: id === 53 ? '晨星组' : '北辰组' }), model: 'gpt-b' } })
    await flushPromises()
    document.querySelector<HTMLElement>('[data-qp-decisions-more]')!.click()
    await flushPromises()
    expect(entries()).toEqual(['90', '89', '80', '79', '70', '60', '50', '40'])
    const what = (id: number) => entry(id).querySelector('.drow__what')!
    const legacy = (action: string) => `admin.schedQuality.decisions.legacy:${JSON.stringify({ action: `admin.schedQuality.decisions.action.${action}` })}`
    expect([90, 89, 80, 79, 70, 60, 50, 40].map((id) => what(id).textContent)).toEqual([
      'admin.schedQuality.decisions.action.enter',
      legacy('enter'),
      'admin.schedQuality.decisions.action.trial_pass',
      'admin.schedQuality.decisions.deadline',
      'admin.schedQuality.decisions.action.trial_fail',
      // The previous rules' end at the deadline is their trial without a verdict, not 到期回正常; their level clear too.
      legacy('trial_timeout'),
      legacy('clear'),
      // A line without a basis has no version: the previous rules'.
      legacy('renew')
    ])
    // Each record of the mixed minutes names only its own group.
    expect([90, 89, 80, 79].map((id) => entry(id).querySelector('.drow__groups')!.getAttribute('title'))).toEqual(['晨星组', '北辰组', '晨星组', '北辰组'])
    // A record of the previous rules with a line keeps its colour and evidence, as before.
    expect([...what(89).classList]).toContain('drow__what--rear')
    expect(entry(89).querySelector('button')).not.toBeNull()
    // At its deadline under 4.0 the line under it still says why it was not released sooner.
    const J = 'admin.schedQuality.decisions.timeout.joiner'
    expect(document.querySelector('[data-qp-trial-timeout="79"]')!.textContent).toBe(
      `admin.schedQuality.decisions.timeout.line:${JSON.stringify({ reasons: `admin.schedQuality.decisions.timeout.noTraffic${J}admin.schedQuality.decisions.timeout.seats.full` })}`
    )
  })

  // Plan item 20's acceptance line: one line at the rear reads the same in the panel's cell, the list's tip card and the
  // opened decision — the same basis through the same function — for the cache line and for a first-output line, whose
  // line is the tolerance its decision was taken with, never written with the seconds.
  it.each([
    ['cache', basis('cache', { cache: cacheEvidence({ ratio: 0.762, min_ratio: 0.86 }) }), '76.2 / 86'],
    ['ttfo', basis('ttfo', { ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 27, tolerance: 0.175 }) }), '67.5 / 17.5'],
    ['ttfo', basis('ttfo', { rule_version: '3.1', ttft: ttfoEvidence({ limit_s: 8, n: 40, k: 27, z: 0.31 }) }), '67.5 / —']
  ] as const)('shows the %s line\'s reading as one text in the panel, the list\'s card and the opened decision (%s)', async (dim, record, expected) => {
    const value = detail()
    const [, b] = value.groups[0].by_model
    b.lines.ttfo = line('ttfo', dim === 'ttfo' ? 'rear' : 'pass', dim === 'ttfo' ? { until: inMinutes(20), basis: record } : {})
    b.lines.cache = line('cache', dim === 'cache' ? 'rear' : 'unknown', dim === 'cache' ? { until: inMinutes(20), basis: record } : {})
    api.accountDetail.mockResolvedValue(value)
    api.accountDecisions.mockResolvedValue([record90(dim, record)])
    api.accountPerformance.mockImplementation(async (id: number) => perfAnswer([perfRow(id, { group_id: 53, model: 'gpt-b' }, { n: 463, slowRate: 0.531 })]))
    mount(Panel, { attachTo: document.body })
    await openOn(7, { groupId: 53, model: 'gpt-b' })
    const panelText = groupRow(53).querySelector(`[data-qp-cell="${dim}"] [data-qp-judged]`)!.textContent
    entry(90).querySelector<HTMLElement>('.drow__line')!.click()
    await flushPromises()
    const decisionText = entry(90).querySelector('[data-qp-evidence-reading]')!.textContent
    const pills = mount(AccountGroupPills, {
      props: {
        account: { id: 7, name: 'acc-7' } as never,
        groups: [{ id: 53, name: '晨星组', platform: 'openai', status: 'active' } as AdminGroup],
        quality: {
          group_status: { 53: 'rear' }, stop: null, trial: [], exemptions: [],
          rear: [{ group_id: 53, model: 'gpt-b', lines: [dim], until_line: dim, until: inMinutes(20), level: 0, exempt: false, effective: true, line_details: [{ line: dim, identity: 'rear', until: inMinutes(20), basis: record }] }]
        }
      }
    })
    const tip = (pills.get('[data-test="group-pill-dot"]').element as HTMLElement & { __tipText: () => TipCard }).__tipText()
    const cardText = (tip.rows![0][2] as { text: string }).text
    expect([panelText, cardText, decisionText]).toEqual([expected, expected, expected])
    // Never the seconds of the limit (8 s) beside the share.
    expect(cardText).not.toMatch(/秒|\d\s?s\b/)
  })

  // Plan item 20: the version in force is the detail's current_rule_version, handed to the decisions by the panel.
  it('reads the rule version in force from the account\'s detail', async () => {
    const later = detail()
    later.current_rule_version = '4.1'
    api.accountDetail.mockResolvedValue(later)
    api.accountDecisions.mockResolvedValue([record(90, { at: inMinutes(-1), basis: basis('ttfo') })])
    mount(Panel, { attachTo: document.body })
    await openOn()
    expect(entry(90).querySelector('.drow__what')!.textContent).toBe(
      'admin.schedQuality.decisions.legacy:{"action":"admin.schedQuality.decisions.action.enter"}'
    )
  })

  // Astra review of fe-min, suggestion 2: one minute, one action, three groups with a 4.0 end at the deadline, a 4.0 end
  // for a reason the copy does not know, and a 3.1 end at the deadline: three lines, each titled, grouped and opened on
  // its own. Only a trial's end is named by its reason; any other action reads by itself.
  it('titles same-action records of one minute by their own version and reason, each with its group and why', async () => {
    atLocalNoon()
    const at = inMinutes(-1)
    const ended = (rule_version: string, release_reason: string, samples_local: number, trial_seats: 'full' | 'not_observed') =>
      ({ action: 'trial_timeout', until: null, basis: basis('ttfo', { rule_version, kind: 'trial', release_reason, samples_local, trial_seats }) })
    api.accountDecisions.mockResolvedValueOnce([
      record(90, { at, group_id: 53, ...ended('4.0', 'deadline', 0, 'full') }),
      record(89, { at, group_id: 54, ...ended('4.0', 'later', 0, 'not_observed') }),
      record(88, { at, group_id: 55, ...ended('3.1', 'deadline', 0, 'full') }),
      record(80, { at: inMinutes(-10), action: 'renew', basis: basis('ttfo', { release_reason: 'deadline' }) })
    ])
    const names: Record<number, string> = { 53: '晨星组', 54: '北辰组', 55: '次元组' }
    mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, ruleVersion: '4.0', group: (id: number) => ({ name: names[id] }), model: 'gpt-b' } })
    await flushPromises()
    expect(entries()).toEqual(['90', '89', '88', '80'])
    expect([90, 89, 88, 80].map((id) => entry(id).querySelector('.drow__what')!.textContent)).toEqual([
      'admin.schedQuality.decisions.deadline',
      'admin.schedQuality.decisions.action.trial_timeout',
      'admin.schedQuality.decisions.legacy:{"action":"admin.schedQuality.decisions.action.trial_timeout"}',
      'admin.schedQuality.decisions.action.renew'
    ])
    expect([90, 89, 88].map((id) => entry(id).querySelector('.drow__groups')!.getAttribute('title'))).toEqual(['晨星组', '北辰组', '次元组'])
    const J = 'admin.schedQuality.decisions.timeout.joiner'
    const why = (seats: string) =>
      `admin.schedQuality.decisions.timeout.line:${JSON.stringify({ reasons: `admin.schedQuality.decisions.timeout.noTraffic${J}admin.schedQuality.decisions.timeout.seats.${seats}` })}`
    for (const [id, seats] of [[90, 'full'], [89, 'not_observed'], [88, 'full']] as const) {
      const line = entry(id).querySelector<HTMLElement>('.drow__line')!
      line.click()
      await flushPromises()
      expect(line.getAttribute('aria-expanded')).toBe('true')
      expect(entry(id).querySelectorAll('[data-qp-trial-timeout]')).toHaveLength(1)
      expect(entry(id).querySelector(`[data-qp-trial-timeout="${id}"]`)!.textContent).toBe(why(seats))
      expect(entry(id).querySelector('.drow__group')).toBeNull()
    }
  })

  // Astra 2: records merged by the minute shown keep their own ends and evidence, to the second, when opened.
  it('keeps every merged record\'s own end and evidence when their ends share the minute shown', async () => {
    atLocalNoon()
    const at = inMinutes(-1)
    const end = (seconds: number) => new Date(2026, 10, 19, 12, 20, seconds).toISOString()
    api.accountDecisions.mockResolvedValueOnce([
      record(90, { at, group_id: 53, until: end(1), basis: basis('ttfo', { samples_local: 4 }) }),
      record(89, { at, group_id: 54, until: end(59), basis: basis('ttfo', { samples_local: 9 }) })
    ])
    mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, ruleVersion: '4.0', group: (id: number) => ({ name: id === 53 ? '晨星组' : '北辰组' }) } })
    await flushPromises()
    expect(entries()).toEqual(['90'])
    // Evidence that differs between groups shows apart, each with its group and its own end to the second.
    const groups = Array.from(entry(90).querySelectorAll('[data-qp-decision-item]'), (el) => [
      el.querySelector('.drow__group .sr-only')!.textContent, el.querySelector('.drow__group-end')!.textContent,
      Array.from(el.querySelectorAll('[data-qp-evidence-samples] dd'), (cell) => cell.textContent).join(' ')
    ])
    expect(groups).toEqual([
      ['晨星组', '12:20:01', '30 4'],
      ['北辰组', '12:20:59', '30 9']
    ])
  })

  // A first-page refresh that fails drops the old list, so its retry asks for the first page (no before_id) again.
  it('retries the first page after a refresh fails, never paging on from the records shown before', async () => {
    api.accountDecisions
      .mockResolvedValueOnce(Array.from({ length: 50 }, (_, i) => record(60 - i)))
      .mockRejectedValueOnce(new Error('down'))
      .mockResolvedValueOnce([record(61, { action: 'reset', until: null }), record(60)])
    const w = mount(SchedDecisions, { attachTo: document.body, props: { accountId: 7, ruleVersion: '4.0', group: () => ({ name: '晨星组' }) } })
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
