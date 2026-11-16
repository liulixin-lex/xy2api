import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { BackendOrder, BackendOrderRow } from '@/api/admin/schedQualityTypes'
import orderFixture from '@/__tests__/fixtures/schedQuality/order.json'
import GroupSchedBoard from '../GroupSchedBoard.vue'
import { closeSchedBoard, markSchedChanged, openSchedBoard, setSchedBoardModel, useSchedLayers } from '@/composables/useSchedLayers'
import { withSchedHost } from '@/components/admin/account/quality/__tests__/schedLayerHost'

const api = vi.hoisted(() => ({ groupBoard: vi.fn(), groupsSummary: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const groupsApi = vi.hoisted(() => ({ setMemberPinned: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: groupsApi } }))
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
    te: (key: string) => !key.endsWith('.brand_new')
  })
}))

enableAutoUnmount(afterEach)

const base = (orderFixture as BackendOrder).rows[0]
const row = (id: number, patch: Partial<BackendOrderRow>): BackendOrderRow => ({ ...structuredClone(base), account_id: id, name: `acc-${id}`, ...patch })

// The backend's selection order: front first, then the rear, then accounts that take no part (stopped).
const REAR_LINES = { ttfo: 'rear', error: 'pass', cache: 'rear' } as const
function board(): BackendOrder {
  return {
    ...(structuredClone(orderFixture) as BackendOrder),
    rows: [
      row(7, { rank: 1 }),
      row(5, { rank: 2, exempt: true }),
      row(3, { rank: 3, layer: 'rear', lines: { ...REAR_LINES }, until: new Date(Date.now() + 90_000).toISOString() }),
      row(9, { rank: 4, layer: 'stopped', lines: { ttfo: 'pass', error: 'rear', cache: 'pass' }, until: new Date(Date.now() + 130_000).toISOString() }),
      row(1, { rank: 5, layer: 'stopped' })
    ]
  }
}

const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-board-row]'))
// The board inside a page whose live table fetches its data (the groups page).
const Board = withSchedHost(GroupSchedBoard)

beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedBoard()
  perms.granted = ['accounts:view', 'groups:view']
  document.documentElement.dataset.motion = 'reduced'
  api.groupBoard.mockReset().mockResolvedValue(board())
  api.groupsSummary.mockReset().mockResolvedValue({ at: '', stale: false, groups: [] })
  groupsApi.setMemberPinned.mockReset()
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('GroupSchedBoard data from the page\'s live table (7f-1)', () => {
  it('asks nothing while closed, at once on opening and on another model, and drops the previous model\'s late answer', async () => {
    mount(Board, { attachTo: document.body })
    await flushPromises()
    expect(api.groupBoard).not.toHaveBeenCalled()

    openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
    await flushPromises()
    expect(api.groupBoard).toHaveBeenCalledTimes(1)

    let late!: (value: BackendOrder) => void
    api.groupBoard.mockReturnValueOnce(new Promise((resolve) => { late = resolve }))
    setSchedBoardModel('gpt-6-astra')
    await flushPromises()
    expect(api.groupBoard).toHaveBeenLastCalledWith(53, 'gpt-6-astra', expect.anything())
    // The answer for the model picked before is not shown under the new one.
    setSchedBoardModel('gpt-6.1-sol')
    await flushPromises()
    late({ ...board(), rows: [row(42, { rank: 1 })] })
    await flushPromises()
    expect(rows().map((tr) => tr.dataset.sqBoardRow)).toEqual(['7', '5', '3', '9', '1'])
  })
})

describe('GroupSchedBoard', () => {
  it('renders the accounts in the order the API gives, never re-sorted', async () => {
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
    await flushPromises()
    expect(api.groupBoard).toHaveBeenCalledWith(53, 'gpt-6.1-sol', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(rows().map((tr) => tr.dataset.sqBoardRow)).toEqual(['7', '5', '3', '9', '1'])
    expect(rows().map((tr) => tr.querySelector('td')!.textContent)).toEqual(['1', '2', '3', '—', '—'])
  })

  it('shows stopped rows weak at the end with a countdown, a rear row with its countdown and the lines at the rear', async () => {
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const [normal, exempt, back, stopped, stoppedNoEnd] = rows()
    expect(normal.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.layer.front')
    expect(exempt.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.exempted')
    expect(back.querySelector('[data-sq-state]')!.textContent).toMatch(/^admin\.schedQuality\.layer\.rear 1:(29|30)$/)
    expect(Array.from(back.querySelectorAll('[data-sq-line]'), (el) => (el as HTMLElement).dataset.identity)).toEqual(['rear', 'pass', 'rear'])
    expect(Array.from(back.querySelectorAll('[data-sq-line]'), (el) => el.classList.contains('sq-shape--fail'))).toEqual([true, false, true])

    expect(stopped.classList.contains('sq-weak')).toBe(true)
    expect(stopped.classList.contains('sq-split')).toBe(true)
    expect(stopped.querySelector('[data-sq-state]')!.textContent).toMatch(/^admin\.schedQuality\.reject\.not_schedulable 2:(09|10)$/)
    // A stopped row marks no dimension.
    expect(stopped.querySelectorAll('.sq-shape')).toHaveLength(0)
    expect(stoppedNoEnd.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.reject.not_schedulable')
    expect(stoppedNoEnd.classList.contains('sq-split')).toBe(false)
  })

  it('names the trial layers, the reason an account is not used, and greys out an unknown line', async () => {
    const soon = (s: number) => new Date(Date.now() + s * 1000).toISOString()
    const trialLines = { ttfo: 'trial', error: 'pass', cache: 'unknown' } as const
    api.groupBoard.mockResolvedValue({
      ...board(),
      rows: [
        row(4, { rank: 1, layer: 'trial', lines: { ...trialLines }, until: soon(600) }),
        row(6, { rank: 2, layer: 'trial_full', lines: { ...trialLines }, until: soon(60) }),
        row(8, { rank: 3, excluded: 'profit_floor' })
      ]
    })
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const [trial, full, excluded] = rows()
    const state = (tr: HTMLElement) => tr.querySelector('[data-sq-state]')!.textContent
    expect(state(trial)).toMatch(/^admin\.schedQuality\.layer\.trial (9:59|10:00)$/)
    expect(state(full)).toMatch(/^admin\.schedQuality\.layer\.trial_full (0:59|1:00)$/)
    expect(trial.querySelector('[data-sq-line="ttfo"]')!.classList.contains('sq-shape--trial')).toBe(true)
    expect(trial.querySelector('[data-sq-line="cache"]')!.textContent).toBe('—')
    // Excluded by the rate range: not used at all, so it reads weak, has no place and names why.
    expect(state(excluded)).toBe('admin.schedQuality.board.excluded:{"reason":"admin.schedQuality.reject.profit_floor"}')
    expect(excluded.classList.contains('sq-weak')).toBe(true)
    expect(excluded.querySelector('td')!.textContent).toBe('—')
  })

  it('pins an account in the group at once with groups:manage; a failure leaves it and says so', async () => {
    perms.granted = ['accounts:view', 'groups:view', 'groups:manage']
    groupsApi.setMemberPinned.mockResolvedValueOnce({ group_id: 53, account_id: 7, pinned: true }).mockRejectedValueOnce(new Error('boom'))
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const pin = (id: number) => document.querySelector<HTMLButtonElement>(`[data-sq-pin="${id}"]`)!
    expect(pin(7).getAttribute('aria-checked')).toBe('false')
    const asked = api.groupBoard.mock.calls.length
    pin(7).click()
    await flushPromises()
    expect(groupsApi.setMemberPinned).toHaveBeenCalledWith(53, 7, true)
    // The board is asked again; the switch shows the saved state meanwhile.
    expect(api.groupBoard.mock.calls.length).toBeGreaterThan(asked)
    expect(pin(7).getAttribute('aria-checked')).toBe('true')

    // A failed request leaves the switch as it was and says why, in the backend's words.
    groupsApi.setMemberPinned.mockReset().mockRejectedValueOnce({ status: 400, message: 'account 5 is not a member of group 53' })
    pin(5).click()
    await flushPromises()
    expect(groupsApi.setMemberPinned).toHaveBeenLastCalledWith(53, 5, true)
    expect(pin(5).getAttribute('aria-checked')).toBe('false')
    expect(document.querySelector('[data-sq-pin-failed="5"]')!.textContent).toBe('admin.schedQuality.result.failed · account 5 is not a member of group 53')
  })

  it('keeps a saved pin through board answers that still show the old state, until one agrees', async () => {
    perms.granted = ['accounts:view', 'groups:view', 'groups:manage']
    groupsApi.setMemberPinned.mockResolvedValue({ group_id: 53, account_id: 7, pinned: true })
    // The scheduler's snapshot follows a few seconds later: fresh answers that still say "not pinned".
    api.groupBoard.mockImplementation(async () => board())
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const pin = () => document.querySelector<HTMLButtonElement>('[data-sq-pin="7"]')!
    pin().click()
    await flushPromises()
    markSchedChanged()
    await flushPromises()
    expect(pin().getAttribute('aria-checked')).toBe('true')

    // Then the board agrees; a later unpin by someone else shows as it is.
    api.groupBoard.mockImplementation(async () => ({ ...board(), rows: board().rows.map((r) => (r.account_id === 7 ? { ...r, pinned: true } : r)) }))
    markSchedChanged()
    await flushPromises()
    expect(pin().getAttribute('aria-checked')).toBe('true')
    api.groupBoard.mockImplementation(async () => board())
    markSchedChanged()
    await flushPromises()
    expect(pin().getAttribute('aria-checked')).toBe('false')
  })

  it('reads a row without `excluded` as used, and shows 其他 for a layer or identity it does not know', async () => {
    const { excluded: _absent, ...plain } = row(7, { rank: 1 })
    api.groupBoard.mockResolvedValue({
      ...board(),
      rows: [plain as BackendOrderRow, row(4, { rank: 2, layer: 'brand_new' as never, lines: { ttfo: 'brand_new' as never, error: 'pass', cache: 'pass' } })]
    })
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const [first, odd] = rows()
    expect(first.classList.contains('sq-weak')).toBe(false)
    expect(first.querySelector('td')!.textContent).toBe('1')
    expect(first.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.layer.front')
    expect(odd.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.unknown')
    expect(odd.querySelector('[data-sq-line="ttfo"]')!.getAttribute('aria-label')).toBe('admin.schedQuality.dim.ttfo admin.schedQuality.unknown')
  })

  it('heads the board with the group over all models: identities, the users\' waits from the gateway and from the body, migration 400s', async () => {
    api.groupsSummary.mockResolvedValue({
      at: '', stale: false,
      groups: [{
        group_id: 53,
        accounts: { pass: 5, unknown: 2, trial: 1, rear: 1 },
        last60: { e2e_first_output_p50_ms: 3200, e2e_first_output_p90_ms: 8100, body_first_output_p50_ms: 2900, body_first_output_p90_ms: null, requests: 420, migration_400: 3 }
      }]
    })
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
    await flushPromises()
    expect(api.groupsSummary).toHaveBeenCalledWith([53], expect.objectContaining({ signal: expect.any(AbortSignal) }))
    const text = (key: string) => document.querySelector(`[data-sq-summary="${key}"]`)!.textContent
    expect(document.querySelector('[data-sq-board-summary] dt')!.textContent).toBe('admin.schedQuality.board.summaryTitle')
    expect(text('accounts')).toBe('admin.schedQuality.board.summaryCounts:{"pass":"5","unknown":"2","trial":"1","rear":"1"}')
    expect(text('e2e')).toBe('admin.schedQuality.board.summaryFromGateway:{"p50":"3.2s","p90":"8.1s"}')
    expect(text('body')).toBe('admin.schedQuality.board.summaryFromBody:{"p50":"2.9s","p90":"—"}')
    expect(text('migration')).toBe('admin.schedQuality.board.summaryMigration:{"n":"3"}')
  })

  it('shows "—" in the summary when it has no data or did not load, and still shows the board', async () => {
    api.groupsSummary.mockRejectedValue(new Error('down'))
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    expect(rows()).toHaveLength(5)
    const text = (key: string) => document.querySelector(`[data-sq-summary="${key}"]`)!.textContent
    expect(text('accounts')).toBe('admin.schedQuality.board.summaryCounts:{"pass":"—","unknown":"—","trial":"—","rear":"—"}')
    expect(text('e2e')).toBe('admin.schedQuality.board.summaryFromGateway:{"p50":"—","p90":"—"}')
    expect(text('migration')).toBe('admin.schedQuality.board.summaryMigration:{"n":"—"}')
  })

  it('shows a pinned account as text without groups:manage', async () => {
    api.groupBoard.mockResolvedValue({ ...board(), rows: [row(7, { rank: 1, pinned: true }), row(5, { rank: 2 })] })
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    expect(document.querySelector('[data-sq-pin]')).toBeNull()
    expect(rows()[0].querySelector('[data-sq-pinned]')!.textContent).toBe('admin.schedQuality.board.pinned')
    expect(rows()[1].querySelector('[data-sq-pinned]')).toBeNull()
  })

  it('opens the account panel on the board\'s model from an account link', async () => {
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'alias-a')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-trigger="account:3"]')!.click()
    const { drawer } = useSchedLayers()
    expect([drawer.accountId, drawer.model]).toEqual([3, 'gpt-6.1-sol'])
  })

  it('shows account names as text without accounts:view', async () => {
    perms.granted = ['groups:view']
    mount(Board, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    expect(document.querySelector('[data-sq-trigger^="account:"]')).toBeNull()
    expect(rows()[0].querySelectorAll('td')[1].textContent!.trim()).toBe('acc-7')
  })

  for (const theme of ['light', 'dark']) {
    it(`keeps the open model menu through a refresh: arrows still move, Escape closes only the menu (${theme})`, async () => {
      document.documentElement.classList.toggle('dark', theme === 'dark')
      mount(Board, { attachTo: document.body })
      await flushPromises()
      openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
      await flushPromises()
      const trigger = document.querySelector<HTMLButtonElement>('[data-base-dialog] .modal-header button[aria-haspopup]')!
      trigger.focus()
      trigger.click()
      await flushPromises()
      const menu = document.querySelector<HTMLElement>('[role="listbox"]')!
      expect(menu.closest('[data-base-dialog]')).toBeNull()
      expect(document.activeElement).toBe(menu)
      const highlighted = () => Array.from(menu.querySelectorAll('[role="option"]')).findIndex((el) => el.classList.contains('select-option-focused'))
      const arrowDown = () => menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
      arrowDown()
      await flushPromises()
      const before = highlighted()
      expect(before).toBe(1)

      // A refresh of the board lands while the menu is open.
      api.groupBoard.mockResolvedValue({ ...board(), at: new Date().toISOString() })
      markSchedChanged()
      await flushPromises()
      expect(api.groupBoard).toHaveBeenCalledTimes(2)
      expect(document.activeElement).toBe(menu)
      // The refreshed options keep the highlighted model.
      expect(highlighted()).toBe(before)

      arrowDown()
      await flushPromises()
      expect(highlighted()).toBe(0)

      menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await flushPromises()
      // The menu is leaving (jsdom never ends the leave transition).
      expect(trigger.getAttribute('aria-expanded')).toBe('false')
      expect(useSchedLayers().board.groupId).toBe(53)
      expect(document.activeElement).toBe(trigger)

      // The next Escape is the board's.
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await flushPromises()
      expect(useSchedLayers().board.groupId).toBeNull()
      document.documentElement.classList.remove('dark')
    })
  }
})
