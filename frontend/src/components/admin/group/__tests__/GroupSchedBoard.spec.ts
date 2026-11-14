import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { BackendOrder, BackendOrderRow } from '@/api/admin/schedQualityTypes'
import orderFixture from '@/__tests__/fixtures/schedQuality/order.json'
import GroupSchedBoard from '../GroupSchedBoard.vue'
import { closeSchedBoard, markSchedChanged, openSchedBoard, useSchedLayers } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ groupBoard: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const perms = vi.hoisted(() => ({ granted: [] as string[] }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: (module: string) => ({ canView: computed(() => perms.granted.includes(`${module}:view`)) }) }
})
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => key !== 'admin.schedQuality.reject.brand_new'
  })
}))

enableAutoUnmount(afterEach)

const base = (orderFixture as BackendOrder).rows[0]
const row = (id: number, patch: Partial<BackendOrderRow>): BackendOrderRow => ({ ...structuredClone(base), account_id: id, name: `acc-${id}`, ...patch })

// The backend's selection order: normal first, then at the back (below), then accounts that take no part (stopped).
function board(): BackendOrder {
  return {
    ...(structuredClone(orderFixture) as BackendOrder),
    rows: [
      row(7, { rank: 1 }),
      row(5, { rank: 2, exempt: true }),
      row(3, { rank: 3, layer: 'below', dims: ['ttfo', 'cache'], until: new Date(Date.now() + 90_000).toISOString() }),
      row(9, { rank: 4, layer: 'stopped', dims: ['error'], until: new Date(Date.now() + 130_000).toISOString() }),
      row(1, { rank: 5, layer: 'stopped' })
    ]
  }
}

const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-board-row]'))

beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedBoard()
  perms.granted = ['accounts:view', 'groups:view']
  document.documentElement.dataset.motion = 'reduced'
  api.groupBoard.mockReset().mockResolvedValue(board())
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('GroupSchedBoard', () => {
  it('renders the accounts in the order the API gives, never re-sorted', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
    await flushPromises()
    expect(api.groupBoard).toHaveBeenCalledWith(53, 'gpt-6.1-sol', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(rows().map((tr) => tr.dataset.sqBoardRow)).toEqual(['7', '5', '3', '9', '1'])
    expect(rows().map((tr) => tr.querySelector('td')!.textContent)).toEqual(['1', '2', '3', '—', '—'])
  })

  it('shows stopped rows weak at the end with a countdown, a row at the back with its countdown and failing dimensions', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const [normal, exempt, back, stopped, stoppedNoEnd] = rows()
    expect(normal.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.normal')
    expect(exempt.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.exempted')
    expect(back.querySelector('[data-sq-state]')!.textContent).toMatch(/^admin\.schedQuality\.state\.cooling 1:(29|30)$/)
    expect(Array.from(back.querySelectorAll('td:nth-last-child(-n+3) > span'), (el) => el.classList.contains('sq-shape--fail'))).toEqual([true, false, true])

    expect(stopped.classList.contains('sq-weak')).toBe(true)
    expect(stopped.classList.contains('sq-split')).toBe(true)
    expect(stopped.querySelector('[data-sq-state]')!.textContent).toMatch(/^admin\.schedQuality\.reject\.not_schedulable 2:(09|10)$/)
    // A stopped row marks no dimension.
    expect(stopped.querySelectorAll('.sq-shape')).toHaveLength(0)
    expect(stoppedNoEnd.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.reject.not_schedulable')
    expect(stoppedNoEnd.classList.contains('sq-split')).toBe(false)
  })

  it('opens the account panel on the board\'s model from an account link', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'alias-a')
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-trigger="account:3"]')!.click()
    const { drawer } = useSchedLayers()
    expect([drawer.accountId, drawer.model]).toEqual([3, 'gpt-6.1-sol'])
  })

  it('shows account names as text without accounts:view', async () => {
    perms.granted = ['groups:view']
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    expect(document.querySelector('[data-sq-trigger^="account:"]')).toBeNull()
    expect(rows()[0].querySelectorAll('td')[1].textContent!.trim()).toBe('acc-7')
  })

  for (const theme of ['light', 'dark']) {
    it(`keeps the open model menu through a refresh: arrows still move, Escape closes only the menu (${theme})`, async () => {
      document.documentElement.classList.toggle('dark', theme === 'dark')
      mount(GroupSchedBoard, { attachTo: document.body })
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
