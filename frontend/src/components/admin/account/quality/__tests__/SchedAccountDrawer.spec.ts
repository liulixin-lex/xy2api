import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedAccountDetail } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn(), reset: vi.fn() }))
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
      model: 'gpt-6.1-sol', state: 'cooling', level: 1, reason: 'cache', since: '2026-11-11T12:00:00Z', until: null,
      ttfo: { verdict: 'fail', from: 'real', n: 4, p90_ms: 14200, p90_timeout: false, limit_ms: 10000 },
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

    // Own window: the numbers given by the API and the cause square for the cooling dimension.
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

  it('hides write actions without accounts:manage and shows the group name as text without groups:view', async () => {
    perms.granted = ['accounts:view']
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    for (const trigger of ['drawer-exempt', 'drawer-reset', 'auto:model_limit:gpt-6-astra', 'override:53', 'board:53']) {
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
    d.all_groups_override = { mode: 'exempt', all_groups: true, expires_at: null, reason: '' }
    d.groups[0].override = d.all_groups_override
    api.accountDetail.mockResolvedValue(d)
    api.clearOverride.mockRejectedValue({ status: 500 })
    mount(SchedAccountDrawer, { attachTo: document.body })
    openSchedDrawer({ id: 479, name: 'acc-03' })
    await flushPromises()
    const header = document.querySelector<HTMLElement>('[data-sq-trigger="drawer-exempt"]')!
    expect(header.textContent).toBe('admin.schedQuality.action.undoExempt')
    header.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 0)
    expect(header.previousElementSibling?.textContent).toBe('admin.schedQuality.result.failed')
    expect(row(53).textContent).not.toContain('admin.schedQuality.result.failed')
  })

  it('shows unknown verdicts, states and outcomes as the fallback word in a neutral style', async () => {
    const d = detail()
    d.groups[0].by_model[0].ttfo.verdict = 'future' as never
    Object.assign(d.groups[1].by_model[0], { state: 'future' })
    d.groups[1].by_model[0].error.verdict = 'future' as never
    Object.assign(d.recent_attempts[0], { outcome: 'future' })
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
    expect(first[3].textContent).toBe('admin.schedQuality.unknown')
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
    document.querySelector<HTMLElement>('[data-sq-trigger="drawer-exempt"]')!.click()
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
    expect(trigger('override:55')!.textContent).toBe('admin.schedQuality.action.exempt')

    trigger('override:55')!.click()
    await flushPromises()
    const after = structuredClone(d)
    after.groups[2].override = { mode: 'exempt', all_groups: false, expires_at: null, reason: '' }
    api.accountDetail.mockResolvedValue(after)
    document.querySelector<HTMLElement>('[data-sq-popover] .btn-primary')!.click()
    await flushPromises()
    expect(api.setOverride).toHaveBeenCalledWith(479, expect.objectContaining({ group_id: 55, mode: 'exempt' }))

    expect(trigger('override:55')!.textContent).toBe('admin.schedQuality.action.undoExempt')
    // The override shows next to the group name; still no state word and no numbers.
    expect(row(55).querySelector('td:first-child [data-sq-override="exempt"]')).not.toBeNull()
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
    trigger('drawer-exempt')!.click()
    await flushPromises()
    expect(api.clearOverride).toHaveBeenCalledWith(479, 0)
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
    d.groups.push({ group_id: 55, name: '南风组', gate_enabled: false, policy: d.groups[0].policy, override: { mode: 'exempt', all_groups: false, expires_at: null, reason: '' }, by_model: [] })
    perms.granted = ['accounts:view', 'groups:view']
    await openWith(d)
    const capsule = row(55).querySelector('[data-sq-override]')!
    expect([capsule.getAttribute('data-sq-override'), capsule.textContent!.trim()]).toEqual(['exempt', 'admin.schedQuality.state.exempted'])
    expect(trigger('override:55')).toBeNull()
    // Still no state word and no numbers.
    expect(Array.from(row(55).querySelectorAll('td')).slice(2, 5).map((td) => td.textContent!.trim())).toEqual(['', '', ''])

    closeSchedDrawer()
    perms.granted = ALL
    await openWith(d)
    expect(row(55).querySelector('[data-sq-override="exempt"]')).not.toBeNull()
    expect(trigger('override:55')!.textContent).toBe('admin.schedQuality.action.undoExempt')
  })

  it('shows the override in effect next to the name on every row, with or without state rows, to every reader', async () => {
    const d = detail()
    // 晨星组 has state rows and its own exemption; 北辰组 has state rows and falls under the all-groups exemption;
    // 南风组 has none and its own exemption.
    d.all_groups_override = { mode: 'exempt', all_groups: true, expires_at: null, reason: '' }
    d.groups[0].override = { mode: 'exempt', all_groups: false, expires_at: null, reason: '' }
    d.groups[1].override = d.all_groups_override
    d.groups.push({ group_id: 55, name: '南风组', gate_enabled: false, policy: d.groups[0].policy, override: { mode: 'exempt', all_groups: false, expires_at: null, reason: '' }, by_model: [] })
    const shown = () => [53, 54, 55].map((id) => row(id).querySelector('td:first-child [data-sq-override]')?.getAttribute('data-sq-override') ?? null)

    perms.granted = ['accounts:view']
    await openWith(d)
    expect(shown()).toEqual(['exempt', 'exempt', 'exempt'])
    // The state words of rows with state rows stay as they were.
    expect(row(53).querySelector('td:nth-child(2)')!.textContent).toContain('admin.schedQuality.state.cooling')
    expect([53, 54, 55].map((id) => trigger(`override:${id}`))).toEqual([null, null, null])

    closeSchedDrawer()
    perms.granted = ALL
    await openWith(d)
    expect(shown()).toEqual(['exempt', 'exempt', 'exempt'])
    expect([53, 54, 55].map((id) => trigger(`override:${id}`)!.textContent)).toEqual([
      'admin.schedQuality.action.undoExempt', 'admin.schedQuality.action.undoExempt', 'admin.schedQuality.action.undoExempt'
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
})
