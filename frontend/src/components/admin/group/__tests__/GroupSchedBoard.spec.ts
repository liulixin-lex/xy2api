import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedOrder } from '@/types/schedQuality'
import orderFixture from '@/__tests__/fixtures/schedQuality/order.json'
import GroupSchedBoard from '../GroupSchedBoard.vue'
import { closeSchedBoard, closeSchedDrawer, markSchedChanged, openSchedBoard, useSchedLayers } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ groupOrder: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
const perms = vi.hoisted(() => ({ granted: [] as string[] }))
vi.mock('@/composables/useAdminPermission', async () => {
  const { computed } = await vi.importActual<typeof import('vue')>('vue')
  return { useAdminPermission: (module: string) => ({ canView: computed(() => perms.granted.includes(`${module}:view`)) }) }
})
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

enableAutoUnmount(afterEach)

const order = () => structuredClone(orderFixture) as SchedOrder
const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-board-row]'))
const state = (row: HTMLElement) => row.querySelector('[data-sq-state]')!.textContent

beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedBoard()
  closeSchedDrawer()
  perms.granted = ['accounts:view', 'groups:view']
  document.documentElement.dataset.motion = 'reduced'
  api.groupOrder.mockReset().mockResolvedValue(order())
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('GroupSchedBoard — selection order (FINAL §1, §7)', () => {
  it('renders the rows in the order the API gives, never re-sorted, with the keys of the order', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
    await flushPromises()
    expect(api.groupOrder).toHaveBeenCalledWith(53, 'gpt-6.1-sol', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(rows().map((tr) => tr.dataset.sqBoardRow)).toEqual(['7', '5', '3', '9'])
    expect(rows().map((tr) => tr.querySelector('td')!.textContent)).toEqual(['1', '2', '3', '—'])
    const cells = Array.from(rows()[0].querySelectorAll('td')).map((td) => td.textContent!.trim())
    expect(cells.slice(3, 6)).toEqual(['1', '0.075', '1 / 10'])
  })

  it('reads the layers: 正常 (exempt marked), 不达标·dims · until in red after a split, 停用 greyed at the end', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const [first, exempt, below, stopped] = rows()
    expect(state(first)).toBe('admin.schedQuality.state.normal')
    expect(exempt.querySelector('[data-sq-override="exempt"]')).not.toBeNull()
    expect(state(below)).toMatch(/^admin\.schedQuality\.state\.substandard·admin\.schedQuality\.dim\.ttfo·admin\.schedQuality\.dim\.cache · \S+/)
    expect(below.querySelector('[data-sq-state]')!.className).toBe('sq-cool')
    expect(below.classList.contains('sq-split')).toBe(true)
    expect(state(stopped)).toBe('admin.schedQuality.state.disabled')
    expect(stopped.classList.contains('sq-weak')).toBe(true)
    expect(stopped.classList.contains('sq-split')).toBe(true)
  })

  it('opens the account drawer at this group and model from an account link', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-trigger="account:3"]')!.click()
    const { drawer } = useSchedLayers()
    expect([drawer.accountId, drawer.groupId, drawer.model]).toEqual([3, 53, 'gpt-6.1-sol'])
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

      // A refresh of the order lands while the menu is open.
      api.groupOrder.mockResolvedValue({ ...order(), at: new Date().toISOString() })
      markSchedChanged()
      await flushPromises()
      expect(api.groupOrder).toHaveBeenCalledTimes(2)
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

      // The next Escape is the dialog's.
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await flushPromises()
      expect(useSchedLayers().board.groupId).toBeNull()
      document.documentElement.classList.remove('dark')
    })
  }
})
