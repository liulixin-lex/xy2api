import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { BackendOrder } from '@/api/admin/schedQualityTypes'
import orderFixture from '@/__tests__/fixtures/schedQuality/order.json'
import { lastEval, perfAnswer, perfGroup, perfRow } from '@/__tests__/fixtures/schedQuality/performance'
import GroupSchedBoard from '../GroupSchedBoard.vue'
import { closeSchedBoard, markSchedChanged, openSchedBoard, setSchedBoardModel, setSchedBoardView, useSchedLayers } from '@/composables/useSchedLayers'
import { withSchedHost } from '@/components/admin/account/quality/__tests__/schedLayerHost'

const api = vi.hoisted(() => ({ groupBoard: vi.fn(), groupsSummary: vi.fn(), groupPerformance: vi.fn(), accountPerformance: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { setMemberPinned: vi.fn() } } }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: () => ({ canView: computed(() => true), canManage: computed(() => true) }) }
})
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => !key.endsWith('.brand_new')
  })
}))

enableAutoUnmount(afterEach)

const unknown = (latest: ReturnType<typeof lastEval> | null = null) => ({ identity: 'unknown' as const, until: null, last_eval: latest })
const pass = { identity: 'pass' as const, until: null, last_eval: null }
/** Group 18: a slow busy account (red, never assessed), a slow one by a little, a fast one, a short one, one that left. */
function answer() {
  return perfAnswer([
    perfRow(483, { requests: 463, score: { value: 0.251, n: 463, sessions: 30, excess: 155 } }, { n: 463, slowRate: 0.531 }),
    perfRow(615, { requests: 1066, score: { value: 0.089, n: 1066, sessions: 40, excess: 78 } }, { n: 1066, slowRate: 0.27 }),
    perfRow(285, { requests: 5139, priority: 1, score: { value: -0.059, n: 5139, sessions: 90, excess: -658 } }, { n: 5139, slowRate: 0.0686 }),
    perfRow(620, { requests: 9, first: { sessions: 1 }, cont: { turns: 4 }, attempts: { n: 9, timeouts: 0 } }, { n: 7, slowRate: 0.43 }),
    perfRow(510, { requests: 127, member: false, score: { value: 0.312, n: 127, sessions: 9, excess: 41 } }, { n: 127, slowRate: 0.529, expectedRate: 0.207 })
  ])
}

const Board = withSchedHost(GroupSchedBoard)
const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-perf-row]'), (tr) => Number(tr.dataset.sqPerfRow!.split(':')[1]))
const row = (id: number) => document.querySelector<HTMLElement>(`[data-sq-perf-row="18:${id}:gpt-x"]`)!
const cell = (id: number, name: string) => row(id).querySelector<HTMLElement>(`[data-sq-cell="${name}"]`)!
const click = async (selector: string) => {
  document.querySelector<HTMLElement>(selector)!.click()
  await flushPromises()
}

async function openPerf() {
  mount(Board, { attachTo: document.body })
  openSchedBoard({ id: 18, name: '晨星组' })
  await flushPromises()
  await click('[data-sq-board-view="perf"]')
}

beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedBoard()
  setSchedBoardView('order')
  document.documentElement.dataset.motion = 'reduced'
  api.groupBoard.mockReset().mockResolvedValue(structuredClone(orderFixture) as BackendOrder)
  api.groupsSummary.mockReset().mockResolvedValue({ at: '', stale: false, groups: [] })
  api.groupPerformance.mockReset().mockResolvedValue(answer())
  api.accountPerformance.mockReset().mockResolvedValue(perfAnswer([]))
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('GroupSchedBoard 12-hour view', () => {
  it('switches views with the tabs, asks for the 12 hours only in that view, and opens on it again next time', async () => {
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 18, name: '晨星组' })
    await flushPromises()
    expect(api.groupPerformance).not.toHaveBeenCalled()
    expect(document.querySelector('[data-sq-board-table]')).not.toBeNull()

    await click('[data-sq-board-view="perf"]')
    expect(api.groupPerformance).toHaveBeenCalledWith(18, expect.anything())
    expect(document.querySelector('[data-sq-board-table]')).toBeNull()
    expect(document.querySelector('[data-sq-board-view="perf"]')!.getAttribute('aria-selected')).toBe('true')
    // The order rests in this view: another model asks nothing of it.
    const orderCalls = api.groupBoard.mock.calls.length
    setSchedBoardModel('gpt-x')
    await flushPromises()
    expect(api.groupBoard).toHaveBeenCalledTimes(orderCalls)

    closeSchedBoard()
    await flushPromises()
    openSchedBoard({ id: 18, name: '晨星组' })
    await flushPromises()
    expect(useSchedLayers().board.view).toBe('perf')
    expect(document.querySelector('[data-sq-perf-table]')).not.toBeNull()

    await click('[data-sq-board-view="order"]')
    expect(api.groupBoard.mock.calls.length).toBeGreaterThan(orderCalls)
  })

  it('shows the skeleton first, a retry after a failure, and says when the group has no requests', async () => {
    let fail!: (error: Error) => void
    api.groupPerformance.mockReturnValueOnce(new Promise((_, reject) => { fail = reject }))
    await openPerf()
    expect(document.querySelector('[data-sq-perf-loading]')).not.toBeNull()
    fail(new Error('down'))
    await flushPromises()
    expect(document.querySelector('[data-sq-perf-failed]')).not.toBeNull()
    api.groupPerformance.mockResolvedValueOnce(perfAnswer([]))
    await click('[data-sq-perf-failed] button')
    expect(document.querySelector('[data-sq-perf-empty]')!.textContent!.trim()).toBe('admin.schedQuality.perf.empty')
  })

  it('keeps the rows through a failed refresh and says this refresh failed', async () => {
    await openPerf()
    api.groupPerformance.mockRejectedValueOnce(new Error('down'))
    await markSchedChanged()
    await flushPromises()
    expect(rows()).toHaveLength(5)
    expect(document.querySelector('[data-sq-perf-refresh-failed]')).not.toBeNull()
  })

  it('lists by extra slow requests, red cells on the slow and busy one, counts on the short one, the account that left grey', async () => {
    await openPerf()
    expect(rows()).toEqual([483, 615, 510, 285, 620])
    expect(cell(483, 'excess').querySelector('.gperf__red-cell')!.textContent).toContain('+33.4')
    expect(cell(483, 'extra').querySelector('.gperf__red-cell')!.textContent).toBe('+155')
    expect(cell(615, 'excess').querySelector('.gperf__red-cell')).toBeNull()
    expect(cell(285, 'excess').querySelector('.sq-pass-word')).not.toBeNull()
    expect(cell(620, 'slow').textContent!.trim()).toBe('admin.schedQuality.perf.slowCount:{"slow":"3","n":"7"}')
    expect(cell(620, 'score').textContent!.trim()).toBe('admin.schedQuality.perf.scoreShort')
    expect(row(620).classList.contains('sq-weak')).toBe(true)
    expect(row(510).querySelector('[data-sq-perf-left]')).not.toBeNull()
    // The score in points; the rank among scored members of the same priority and model (510 left: no rank, not counted).
    expect(cell(483, 'score').textContent).toContain('25.1')
    expect(cell(483, 'score').textContent).toContain('admin.schedQuality.perf.rank:{"place":2,"of":2}')
    expect(cell(510, 'score').textContent!.trim()).toBe('31.2')
    expect(cell(285, 'score').textContent).toContain('−5.9')
    // The account that left: no verdicts.
    expect(row(510).querySelector('[data-sq-perf-line]')).toBeNull()
  })

  it('sorts by a column header, bad end first, short rows last, with aria-sort on the header', async () => {
    await openPerf()
    await click('[data-sq-perf-sort="cache"]')
    expect(document.querySelector('[data-sq-perf-col="cache"]')!.getAttribute('aria-sort')).toBe('ascending')
    expect(document.querySelector('[data-sq-perf-col="extra"]')!.hasAttribute('aria-sort')).toBe(false)
    expect(rows().at(-1)).toBe(620)
    await click('[data-sq-perf-sort="score"]')
    expect(document.querySelector('[data-sq-perf-col="score"]')!.getAttribute('aria-sort')).toBe('descending')
    expect(rows()).toEqual([510, 483, 615, 285, 620])
    await click('[data-sq-perf-sort="score"]')
    expect(rows()).toEqual([285, 615, 483, 510, 620])
  })

  it('filters by priority and to red rows only, and the model picked in the title narrows the rows', async () => {
    await openPerf()
    expect(Array.from(document.querySelectorAll<HTMLElement>('[data-sq-perf-priority]'), (tab) => tab.dataset.sqPerfPriority)).toEqual(['all', '1', '5'])
    await click('[data-sq-perf-priority="1"]')
    expect(rows()).toEqual([285])
    expect(document.querySelector('[data-sq-perf-red]')).toBeNull()
    await click('[data-sq-perf-priority="all"]')
    const red = document.querySelector<HTMLElement>('[data-sq-perf-red]')!
    expect(red.textContent!.trim()).toBe('admin.schedQuality.perf.redOnly:{"n":2}')
    await click('[data-sq-perf-red]')
    expect(red.getAttribute('aria-pressed')).toBe('true')
    expect(rows()).toEqual([483, 510])
    setSchedBoardModel('gpt-y')
    await flushPromises()
    expect(document.querySelector('[data-sq-perf-empty]')!.textContent!.trim()).toBe('admin.schedQuality.perf.emptyModel:{"model":"gpt-y"}')
  })

  it('opens a row to its detail and keeps it open across a refresh', async () => {
    await openPerf()
    await click('[data-sq-perf-expand="18:483:gpt-x"]')
    const detail = () => document.querySelector('[data-sq-perf-detail="18:483:gpt-x"]')
    expect(detail()).not.toBeNull()
    expect(detail()!.querySelector('[data-sq-perf-detail-item="score"]')!.textContent).toContain('admin.schedQuality.perf.scoreUseFull')
    // Never assessed: each line says there is no assessment on record.
    expect(detail()!.querySelector('[data-sq-perf-detail-item="verdict"]')!.textContent).toContain('admin.schedQuality.perf.detail.evalNone')
    await markSchedChanged()
    await flushPromises()
    expect(detail()).not.toBeNull()
  })

  it('says the gate is off in the group, and shows no verdicts and no reason there', async () => {
    const value = answer()
    value.groups = [perfGroup({ gate_enabled: false })]
    value.rows[0].lines = { ttfo: unknown(lastEval()), error: pass, cache: pass }
    api.groupPerformance.mockResolvedValue(value)
    await openPerf()
    expect(document.querySelector('[data-sq-perf-gate-off]')).not.toBeNull()
    expect(document.querySelector('[data-sq-perf-line]')).toBeNull()
    expect(document.querySelector('[data-sq-perf-why]')).toBeNull()
  })

  // 裁决 4: the seven reasons; over only comes with an action, which says nothing here; an unknown code is other.
  it.each([
    ['peer_short', false, 'admin.schedQuality.noAction.short.peer_short'],
    ['own_short', false, 'admin.schedQuality.noAction.short.own_short'],
    ['within', false, 'admin.schedQuality.noAction.short.within'],
    ['pending', false, 'admin.schedQuality.noAction.short.pending'],
    ['trial_open', false, 'admin.schedQuality.noAction.short.trial_open'],
    ['other', false, 'admin.schedQuality.noAction.short.other'],
    ['brand_new', false, 'admin.schedQuality.noAction.short.other'],
    ['over', true, null]
  ])('writes why a red row was not acted on: %s', async (reason, acted, expected) => {
    const value = answer()
    value.rows[0].lines = { ttfo: unknown(lastEval({ reason, acted, action: acted ? 'renew' : '' })), error: pass, cache: pass }
    api.groupPerformance.mockResolvedValue(value)
    await openPerf()
    expect(row(483).querySelector('[data-sq-perf-why]')?.textContent ?? null).toBe(expected)
  })

  // 裁决 6 on the board: from the row's own counts; nothing when they are missing or null, or this group has samples.
  it('says in the detail when a rear line\'s evidence all came from other groups', async () => {
    const rear = (counts: { samples_local?: number | null; samples_total?: number | null }) => ({ identity: 'rear' as const, until: null, last_eval: null, ...counts })
    const value = answer()
    const lines = (ttfo: ReturnType<typeof rear>) => ({ ttfo, error: pass, cache: pass })
    value.rows[0].lines = lines(rear({ samples_local: 0, samples_total: 14 }))
    value.rows[1].lines = lines(rear({ samples_local: 2, samples_total: 14 }))
    value.rows[2].lines = lines(rear({ samples_local: null, samples_total: null }))
    value.rows[3].lines = lines(rear({}))
    api.groupPerformance.mockResolvedValue(value)
    await openPerf()
    const said = async (id: number) => {
      const key = `18:${id}:gpt-x`
      await click(`[data-sq-perf-expand="${key}"]`)
      return document.querySelector(`[data-sq-perf-detail="${key}"] [data-sq-perf-detail-item="verdict"]`)!.textContent!.includes('admin.schedQuality.perf.detail.elsewhere')
    }
    const shown = []
    for (const id of [483, 615, 285, 620]) shown.push(await said(id))
    expect(shown).toEqual([true, false, false, false])
  })

  it('puts the reason in the mark\'s tip, and no reason on a red row never assessed', async () => {
    const value = answer()
    value.rows[0].lines = { ttfo: unknown(lastEval()), error: pass, cache: pass }
    api.groupPerformance.mockResolvedValue(value)
    await openPerf()
    const mark = row(483).querySelector<HTMLElement>('[data-sq-perf-line="ttfo"]')!
    expect(mark.getAttribute('aria-label')).toMatch(/^admin\.schedQuality\.dim\.ttfo · admin\.schedQuality\.identity\.unknown · admin\.schedQuality\.noAction\.long\.peer_short/)
    expect(row(615).querySelector('[data-sq-perf-why]')).toBeNull()
  })
})
