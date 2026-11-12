import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedAccountDetail, SchedTest } from '@/types/schedQuality'
import { fmtTestTime } from '@/utils/schedQualityFormat'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), startTest: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn(), reset: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const toast = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => toast }))
// Reactive, so a permission change after mount reaches the mounted drawer.
const perms = await vi.hoisted(async () => {
  const { reactive } = await import('vue')
  return reactive({ granted: [] as string[] })
})
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return {
    useAdminPermission: (module: string) => ({
      canView: computed(() => perms.granted.includes(`${module}:view`)),
      canManage: computed(() => perms.granted.includes(`${module}:manage`))
    })
  }
})
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

const ALL = ['accounts:view', 'accounts:manage', 'groups:view']

beforeEach(() => {
  closeSchedDrawer()
  closeSchedBoard()
  perms.granted = ALL
  document.documentElement.dataset.motion = 'reduced'
  Element.prototype.scrollIntoView = vi.fn()
  api.accountDetail.mockReset().mockResolvedValue(detail())
  api.startTest.mockReset()
  api.clearOverride.mockReset()
  api.setOverride.mockReset()
  api.reset.mockReset()
  toast.showSuccess.mockReset()
  toast.showError.mockReset()
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
  it('opens the group board with the request model of the tab, or without a model when the tab has none (K27)', async () => {
    const d = detail()
    d.models[0].request_model = 'alias-sol'
    api.accountDetail.mockResolvedValue(d)
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    const { board } = useSchedLayers()
    panel().querySelector<HTMLElement>('[data-sq-trigger="board:53"]')!.click()
    expect([board.groupId, board.model]).toEqual([53, 'alias-sol'])

    closeSchedBoard()
    d.models[0].request_model = ''
    api.accountDetail.mockResolvedValue(d)
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    panel().querySelector<HTMLElement>('[data-sq-trigger="board:53"]')!.click()
    expect([board.groupId, board.model]).toEqual([53, null])
  })

  it('shows 失败 next to 测试 for a failed test (started + error, or a request error) until the refresh after its own (K28)', async () => {
    api.accountDetail.mockImplementation(async () => detail())
    const wrapper = mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    const mark = () => document.querySelector('[data-sq-test-failed]')
    const runTest = async () => {
      document.querySelector<HTMLElement>('[data-sq-trigger="drawer-test"]')!.click()
      await flushPromises()
    }

    api.startTest.mockResolvedValueOnce({ tests: [
      { model: 'gpt-6.1-sol', test_id: 't1', started: true, why: '' },
      { model: 'gpt-6-astra', test_id: '', started: false, why: 'error' }
    ] })
    await runTest()
    // The refresh the test triggered has landed and the mark is still there.
    expect(api.accountDetail).toHaveBeenCalledTimes(2)
    expect(mark()?.textContent).toBe('admin.schedQuality.result.failed')
    expect(mark()!.className).toContain('sq-fail-word')
    await (wrapper.vm as unknown as { refresh: () => Promise<void> }).refresh()
    await flushPromises()
    expect(mark()).toBeNull()

    api.startTest.mockRejectedValueOnce({ status: 500, message: 'boom' })
    await runTest()
    expect(mark()).not.toBeNull()

    api.startTest.mockResolvedValueOnce({ tests: [{ model: 'gpt-6.1-sol', test_id: 't2', started: true, why: '' }] })
    await runTest()
    expect(mark()).toBeNull()
  })

  it('hides write actions without accounts:manage and shows the group name as text without groups:view', async () => {
    perms.granted = ['accounts:view']
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    for (const trigger of ['drawer-test', 'drawer-demote', 'drawer-exempt', 'drawer-reset', 'auto:model_limit:gpt-6-astra', 'override:53', 'board:53']) {
      expect(document.querySelector(`[data-sq-trigger="${trigger}"]`)).toBeNull()
    }
    expect(row(53).querySelector('td')!.textContent!.trim()).toBe(detail().groups[0].name)
    expect(row(53).querySelector('td button')).toBeNull()

    perms.granted = ['accounts:view', 'accounts:manage']
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    expect(document.querySelector('[data-sq-trigger="override:53"]')).not.toBeNull()
    expect(document.querySelector('[data-sq-trigger="board:53"]')).toBeNull()
  })

  it('shows 失败 next to the header button when cancelling the all-groups override fails', async () => {
    const d = detail()
    d.all_groups_override = { mode: 'demote', all_groups: true, expires_at: null, reason: '' }
    d.groups[0].override = d.all_groups_override
    api.accountDetail.mockResolvedValue(d)
    api.clearOverride.mockRejectedValue({ status: 500 })
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    const header = document.querySelector<HTMLElement>('[data-sq-trigger="drawer-demote"]')!
    expect(header.textContent).toBe('admin.schedQuality.action.undoDemote')
    header.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 0)
    expect(header.previousElementSibling?.textContent).toBe('admin.schedQuality.result.failed')
    expect(row(53).textContent).not.toContain('admin.schedQuality.result.failed')
  })

  it('shows unknown verdicts, states, classes and outcomes as the fallback word in a neutral style', async () => {
    const d = detail()
    d.groups[0].by_model[0].ttfo.verdict = 'future' as never
    Object.assign(d.groups[1].by_model[0], { state: 'future' })
    d.groups[1].by_model[0].error.verdict = 'future' as never
    Object.assign(d.recent_attempts[0], { class: 'future', outcome: 'future' })
    api.accountDetail.mockResolvedValue(d)
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()

    // Same window: the word instead of a shape.
    const ttfo = cells(53)[0].querySelector('span')!
    expect(ttfo.textContent).toBe('admin.schedQuality.unknown')
    expect(ttfo.className).toContain('sq-text-3')
    // Own window: the number uncoloured, read out with the fallback word.
    const error = cells(54)[1].querySelector('span')!
    expect(error.textContent).toBe('2.0%')
    expect(error.className).not.toMatch(/sq-v-/)
    expect(error.getAttribute('aria-label')).toContain('admin.schedQuality.unknown')
    const state = row(54).querySelector('td:nth-child(2) span')!
    expect([state.textContent, state.className]).toEqual(['admin.schedQuality.unknown', 'sq-text-3'])

    const recent = Array.from(panel().querySelectorAll('section'))
      .find((el) => el.querySelector('h3')?.textContent === 'admin.schedQuality.drawer.recent')!
    const first = recent.querySelectorAll('tbody tr')[0].querySelectorAll('td')
    expect([first[2].textContent, first[4].textContent]).toEqual(['admin.schedQuality.unknown', 'admin.schedQuality.unknown'])
    expect(panel().textContent).not.toMatch(/admin\.schedQuality\.(class|result|aria|state)\.future/)
  })

  it('shows the fallback word for an unknown verdict on a dimension without numbers, even when cooling names it', async () => {
    const d = detail()
    d.groups[1].by_model[0].cache.verdict = 'future' as never
    api.accountDetail.mockResolvedValue(d)
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    const cache = cells(54)[2]
    expect(cache.querySelector('.sq-shape--fail')).toBeNull()
    expect(cache.textContent).toContain('admin.schedQuality.unknown')
  })

  it('closes an open override popover when accounts:manage is withdrawn', async () => {
    api.setOverride.mockReset()
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-trigger="drawer-demote"]')!.click()
    await flushPromises()
    expect(document.querySelector('[data-sq-popover]')).not.toBeNull()
    perms.granted = ['accounts:view', 'groups:view']
    await flushPromises()
    expect(document.querySelector('[data-sq-popover]')).toBeNull()
    expect(api.setOverride).not.toHaveBeenCalled()
  })
})

async function openWith(d: SchedAccountDetail, props: Record<string, unknown> = {}) {
  api.accountDetail.mockResolvedValue(d)
  const wrapper = mount(SchedAccountDrawer, { props, attachTo: document.body })
  openSchedDrawer({ id: 479, name: 'acc-03' })
  await flushPromises()
  return wrapper
}
const trigger = (name: string) => document.querySelector<HTMLElement>(`[data-sq-trigger="${name}"]`)

describe('SchedAccountDrawer — member groups and the all-groups override (contract delta D3, scenario 30)', () => {
  it('lists a member group without state rows with only its name and the override action, which sets and cancels', async () => {
    const d = detail()
    d.groups.push({ group_id: 55, name: '南风组', gate_enabled: false, policy: d.groups[0].policy, override: null, by_model: [] })
    await openWith(d)
    const tds = Array.from(row(55).querySelectorAll('td'))
    expect(tds[0].textContent!.trim()).toBe('南风组')
    expect(tds.slice(1, 5).map((td) => td.textContent!.trim())).toEqual(['', '', '', ''])
    expect(trigger('override:55')!.textContent).toBe('admin.schedQuality.action.demote')

    trigger('override:55')!.click()
    await flushPromises()
    const after = structuredClone(d)
    after.groups[2].override = { mode: 'demote', all_groups: false, expires_at: null, reason: '' }
    api.accountDetail.mockResolvedValue(after)
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    expect(api.setOverride).toHaveBeenCalledWith(479, expect.objectContaining({ group_id: 55, mode: 'demote' }))

    expect(trigger('override:55')!.textContent).toBe('admin.schedQuality.action.undoDemote')
    // The override shows next to the group name; still no state word and no numbers.
    expect(row(55).querySelector('td:first-child [data-sq-override="demote"]')).not.toBeNull()
    expect(Array.from(row(55).querySelectorAll('td')).slice(1, 5).map((td) => td.textContent!.trim())).toEqual(['', '', '', ''])
    trigger('override:55')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 55)
  })

  it('reads the all-groups override from the detail root, so an account in no group can still cancel it', async () => {
    const d = detail()
    d.groups = []
    d.all_groups_override = { mode: 'exempt', all_groups: true, expires_at: null, reason: '' }
    await openWith(d)
    const section = panel().querySelector('[data-sq-section="groups"]')!
    expect(section.querySelector('table')).toBeNull()
    expect(section.textContent).toContain('—')
    expect(trigger('drawer-exempt')!.textContent).toBe('admin.schedQuality.action.undoExempt')
    expect(trigger('drawer-demote')!.textContent).toBe('admin.schedQuality.action.demote')
    trigger('drawer-exempt')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 0)
  })
})

describe('SchedAccountDrawer — test area (contract delta E, scenario 31)', () => {
  const at = detailFixture.models[0].test.at
  const head = () => panel().querySelector('[data-sq-test-head]')!
  const words = () => Array.from(head().querySelectorAll('span')).map((el) => el.textContent)
  const table = () => head().closest('section')!.querySelector('table')!
  const tierRows = () => Array.from(table().querySelectorAll('tbody tr')).map((tr) => Array.from(tr.querySelectorAll('td')).map((td) => td.textContent))
  const withTest = (test: Partial<SchedTest>) => {
    const d = detail()
    Object.assign(d.models[0].test!, test)
    return d
  }
  const small = { tier: 'small' as const, ttfo_ms: [3100, 2900], cache_ratio: 0.99, faults: 0, timeouts: 0 }

  it('marks a clean screen 筛查 · time · 完成, with only the small tier and no cache column', async () => {
    await openWith(withTest({ kind: 'screen', clean: true, tiers: [small] }))
    expect(words()).toEqual(['admin.schedQuality.test.screen', fmtTestTime(at), 'admin.schedQuality.test.done'])
    expect(Array.from(table().querySelectorAll('thead th')).map((th) => th.textContent)).toEqual(['admin.schedQuality.col.class', '', 'admin.schedQuality.dim.ttfo', ''])
    expect(tierRows()).toEqual([['admin.schedQuality.tier.small', '', '3.1s', '']])
    expect(table().textContent).not.toContain('99%')
    // No shape or colour that reads as a verdict.
    expect(head().querySelector('[role="img"]')).toBeNull()
  })

  it('marks a full test that ended unclean 三档 · time · 未完成, with three tiers and — for a tier without rounds', async () => {
    const medium = { tier: 'medium' as const, ttfo_ms: [null], cache_ratio: null, faults: 0, timeouts: 1 }
    await openWith(withTest({ kind: 'full', clean: false, auto: false, tiers: [small, medium] }))
    expect(words()).toEqual(['admin.schedQuality.test.full', fmtTestTime(at), 'admin.schedQuality.test.unfinished'])
    expect(tierRows().map((cells) => cells[2])).toEqual(['3.1s', 'admin.schedQuality.result.timeout', '—'])
  })

  it('calls a test without any round 作废', async () => {
    await openWith(withTest({ kind: 'screen', clean: false, done: false, abort_reason: 'not_schedulable', tiers: [] }))
    expect(words()).toEqual(['admin.schedQuality.test.screen', fmtTestTime(at), 'admin.schedQuality.test.aborted'])
  })

  it('shows the kind with 测试中 while the test runs', async () => {
    await openWith(withTest({ kind: 'screen', clean: false, running: true, done: false, tiers: [] }))
    expect(words()).toEqual(['admin.schedQuality.test.screen', 'admin.schedQuality.state.testing'])
  })
})

describe('SchedAccountDrawer — 恢复 (contract delta F2, F3)', () => {
  const withoutCooling = (d: SchedAccountDetail) => {
    d.groups.forEach((group) => group.by_model.forEach((r) => { r.state = 'normal' }))
    return d
  }
  const proxy = { key: 'proxy_circuit:7', kind: 'proxy_circuit', scope: 'proxy' as const, model: '', until: null, count: null, shared: 4, clearable: true, why: '' }

  it('shows 恢复 only for a cooling group row or a clearable auto state other than the shared proxy circuit', async () => {
    await openWith(detail())
    expect(trigger('drawer-reset')).not.toBeNull()

    closeSchedDrawer()
    await openWith(withoutCooling(detail()))
    // The clearable model limit of the fixture.
    expect(trigger('drawer-reset')).not.toBeNull()

    closeSchedDrawer()
    const onlyProxy = withoutCooling(detail())
    onlyProxy.auto_states = [proxy, { ...proxy, key: 'iq:gpt-6-astra', kind: 'iq_gate', scope: 'model', clearable: false }]
    await openWith(onlyProxy)
    expect(trigger('drawer-reset')).toBeNull()
    // The single clear of the proxy circuit is still there.
    expect(trigger('auto:proxy_circuit:7')).not.toBeNull()
  })

  it('runs at once with one toast and no dialog, and stays greyed until the refresh after it is done (scenario 38)', async () => {
    let answer!: (value: unknown) => void
    api.reset.mockReturnValue(new Promise((resolve) => { answer = resolve }))
    let refreshed!: () => void
    const onRecovered = vi.fn(() => new Promise<void>((resolve) => { refreshed = resolve }))
    await openWith(detail(), { onRecovered })
    const loads = api.accountDetail.mock.calls.length

    trigger('drawer-reset')!.click()
    await flushPromises()
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(api.reset).toHaveBeenCalledTimes(1)
    expect(api.reset).toHaveBeenCalledWith(479)
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')
    expect(document.querySelector('.modal-content')).toBeNull()

    api.accountDetail.mockResolvedValue(withoutCooling(detail()))
    answer({ reset_at: 'x', cleared: [], preserved: [{ key: 'session_avoid', kind: 'session_avoid', why: '' }], failed: [] })
    await flushPromises()
    expect(toast.showSuccess.mock.calls).toEqual([['admin.schedQuality.recover.ok:{"name":"…"}']])
    expect(toast.showError).not.toHaveBeenCalled()
    expect(onRecovered).toHaveBeenCalledWith(479)
    expect(api.accountDetail.mock.calls.length).toBeGreaterThan(loads)
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')

    refreshed()
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('false')
    expect(document.querySelector('.modal-content')).toBeNull()
  })
})

describe('SchedAccountDrawer — red-team revisions (R2-RT-FB)', () => {
  it('keeps 恢复 locked until its own detail reload is in, and reloads it once (P0-1)', async () => {
    await openWith(detail(), { onRecovered: async () => {} })
    let reloaded!: (value: SchedAccountDetail) => void
    api.accountDetail.mockReturnValue(new Promise((resolve) => { reloaded = resolve }))
    api.reset.mockResolvedValue({ reset_at: 'x', cleared: [], preserved: [], failed: [] })
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(api.accountDetail).toHaveBeenCalledTimes(2)
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(api.reset).toHaveBeenCalledTimes(1)

    reloaded(detail())
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('false')
    expect(api.accountDetail).toHaveBeenCalledTimes(2)
  })

  it('keeps 恢复 locked while the detail reload that replaced its own is still pending (P0-1, re-check)', async () => {
    const d = detail()
    d.groups[0].override = { mode: 'exempt', all_groups: false, expires_at: null, reason: '' }
    await openWith(d, { onRecovered: async () => {} })
    // Every detail reload waits for the test; a cancelled one fails as the transport does.
    const answers: Array<(value: SchedAccountDetail) => void> = []
    let aborted = 0
    api.accountDetail.mockImplementation((_id: number, { signal }: { signal: AbortSignal }) => new Promise((resolve, reject) => {
      answers.push(resolve)
      signal.addEventListener('abort', () => { aborted++; reject(new DOMException('aborted', 'AbortError')) }, { once: true })
    }))
    api.reset.mockResolvedValue({ reset_at: 'x', cleared: [], preserved: [], failed: [] })
    api.clearOverride.mockResolvedValue(undefined)
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')
    expect(answers).toHaveLength(1)

    // Cancelling the override reloads the detail again: the recovery's reload is aborted and replaced.
    trigger('override:53')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 53)
    expect([answers.length, aborted]).toEqual([2, 1])
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('true')
    trigger('drawer-reset')!.click()
    await flushPromises()
    expect(api.reset).toHaveBeenCalledTimes(1)

    answers[1](detail())
    await flushPromises()
    expect(trigger('drawer-reset')!.getAttribute('aria-busy')).toBe('false')
  })

  it('shows the override of a group without state rows to every reader; only the action needs accounts:manage (P1-2)', async () => {
    const d = detail()
    d.groups.push({ group_id: 55, name: '南风组', gate_enabled: false, policy: d.groups[0].policy, override: { mode: 'demote', all_groups: false, expires_at: null, reason: '' }, by_model: [] })
    perms.granted = ['accounts:view', 'groups:view']
    await openWith(d)
    const capsule = row(55).querySelector('[data-sq-override]')!
    expect([capsule.getAttribute('data-sq-override'), capsule.textContent!.trim()]).toEqual(['demote', 'admin.schedQuality.state.demoted'])
    expect(trigger('override:55')).toBeNull()
    // Still no state word and no numbers.
    expect(Array.from(row(55).querySelectorAll('td')).slice(2, 5).map((td) => td.textContent!.trim())).toEqual(['', '', ''])

    closeSchedDrawer()
    perms.granted = ALL
    await openWith(d)
    expect(row(55).querySelector('[data-sq-override="demote"]')).not.toBeNull()
    expect(trigger('override:55')!.textContent).toBe('admin.schedQuality.action.undoDemote')
  })

  it('shows the override in effect next to the name on every row, with or without state rows, to every reader', async () => {
    const d = detail()
    // 晨星组 has state rows and its own exemption; 北辰组 has state rows and falls under the all-groups demotion;
    // 南风组 has none and its own demotion.
    d.all_groups_override = { mode: 'demote', all_groups: true, expires_at: null, reason: '' }
    d.groups[0].override = { mode: 'exempt', all_groups: false, expires_at: null, reason: '' }
    d.groups[1].override = d.all_groups_override
    d.groups.push({ group_id: 55, name: '南风组', gate_enabled: false, policy: d.groups[0].policy, override: { mode: 'demote', all_groups: false, expires_at: null, reason: '' }, by_model: [] })
    const shown = () => [53, 54, 55].map((id) => row(id).querySelector('td:first-child [data-sq-override]')?.getAttribute('data-sq-override') ?? null)

    perms.granted = ['accounts:view']
    await openWith(d)
    expect(shown()).toEqual(['exempt', 'demote', 'demote'])
    // The state words of rows with state rows stay as they were.
    expect(row(53).querySelector('td:nth-child(2)')!.textContent).toContain('admin.schedQuality.state.cooling')
    expect([53, 54, 55].map((id) => trigger(`override:${id}`))).toEqual([null, null, null])

    closeSchedDrawer()
    perms.granted = ALL
    await openWith(d)
    expect(shown()).toEqual(['exempt', 'demote', 'demote'])
    expect([53, 54, 55].map((id) => trigger(`override:${id}`)!.textContent)).toEqual([
      'admin.schedQuality.action.undoExempt', 'admin.schedQuality.action.undoDemote', 'admin.schedQuality.action.undoDemote'
    ])
  })

  it('shows the all-groups override to every reader, also for an account in no group (P1-2)', async () => {
    const d = detail()
    d.groups = []
    d.all_groups_override = { mode: 'exempt', all_groups: true, expires_at: null, reason: '' }
    perms.granted = ['accounts:view']
    await openWith(d)
    const capsule = panel().querySelector('[data-sq-section="groups"] [data-sq-override="exempt"]')!
    expect(capsule.textContent!.replace(/\s+/g, ' ').trim()).toBe('admin.schedQuality.override.allGroups admin.schedQuality.state.exempted')
    expect(trigger('drawer-exempt')).toBeNull()

    closeSchedDrawer()
    await openWith(detail())
    expect(panel().querySelector('[data-sq-section="groups"] [data-sq-override]')).toBeNull()
  })

  it('gives the test table and the groups table the same fixed widths, so 首字 lines up at any width (P2-1)', async () => {
    await openWith(detail())
    const groups = panel().querySelector<HTMLTableElement>('[data-sq-section="groups"] table')!
    const test = panel().querySelector<HTMLTableElement>('[data-sq-test-head]')!.closest('section')!.querySelector('table')!
    const widths = (table: HTMLTableElement) => Array.from(table.querySelectorAll('col')).map((col) => Number(/w-\[(\d+)px\]/.exec(col.className)?.[1] ?? 0))
    for (const table of [groups, test]) expect(table.className).toMatch(/\btable-fixed\b.*\bmin-w-\[600px\]/)
    // Column 2 is 首字 in both; same width, same distance from the right edge, same flexible first column.
    const g = widths(groups)
    const tt = widths(test)
    expect([g[0], tt[0]]).toEqual([0, 0])
    expect(g[2]).toBe(tt[2])
    expect(g.slice(1).reduce((a, b) => a + b, 0)).toBe(tt.slice(1).reduce((a, b) => a + b, 0))
    expect(g[1]).toBe(tt[1])
    expect(groups.querySelectorAll('thead th')[2].textContent).toBe('admin.schedQuality.dim.ttfo')
    expect(test.querySelectorAll('thead th')[2].textContent).toBe('admin.schedQuality.dim.ttfo')
  })
})
