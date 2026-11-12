import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedAccountDetail } from '@/types/schedQuality'
import detailFixture from '@/__tests__/fixtures/schedQuality/account_detail.json'
import SchedAccountDrawer from '../SchedAccountDrawer.vue'
import { closeSchedBoard, closeSchedDrawer, openSchedDrawer, useSchedLayers } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ accountDetail: vi.fn(), startTest: vi.fn(), setOverride: vi.fn(), clearOverride: vi.fn(), clearAutoState: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const perms = vi.hoisted(() => ({ granted: [] as string[] }))
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
    d.groups[0].override = { mode: 'demote', all_groups: true, expires_at: null, reason: '' }
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
})
