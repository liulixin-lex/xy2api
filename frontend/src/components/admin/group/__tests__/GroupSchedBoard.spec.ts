import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import type { SchedBoard, SchedBoardAccount } from '@/types/schedQuality'
import boardFixture from '@/__tests__/fixtures/schedQuality/board.json'
import policyFixture from '@/__tests__/fixtures/schedQuality/policy.json'
import GroupSchedBoard from '../GroupSchedBoard.vue'
import { closeSchedBoard, openSchedBoard, useSchedLayers } from '@/composables/useSchedLayers'

const api = vi.hoisted(() => ({ groupBoard: vi.fn(), getGroupPolicy: vi.fn() }))
vi.mock('@/api/admin/schedQuality', () => ({ default: api }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key),
    te: (key: string) => key !== 'admin.schedQuality.reject.brand_new'
  })
}))

enableAutoUnmount(afterEach)

const base = (boardFixture as SchedBoard).accounts[0]
const account = (id: number, patch: Partial<SchedBoardAccount>): SchedBoardAccount => ({ ...structuredClone(base), account_id: id, name: `acc-${id}`, ...patch })

function board(): SchedBoard {
  return {
    ...(structuredClone(boardFixture) as SchedBoard),
    accounts: [
      account(7, { rank: 1, can_take_over: false }),
      account(3, { rank: 2, state: 'cooling', until: new Date(Date.now() + 90_000).toISOString(), standing: 'back', verdicts: { ttfo: 'fail', error: 'pass', cache: 'marginal' } }),
      account(5, { rank: 3, override: { mode: 'demote', all_groups: true, expires_at: null, reason: '' }, recently_full_seconds: 12 }),
      account(9, { rank: 0, eligible: false, reject: 'rate_limit', reject_until: new Date(Date.now() + 130_000).toISOString(), verdicts: { ttfo: 'fail', error: 'fail', cache: 'fail' } }),
      account(1, { rank: 0, eligible: false, reject: 'brand_new' })
    ]
  }
}

const rows = () => Array.from(document.querySelectorAll<HTMLElement>('[data-sq-board-row]'))

beforeEach(() => {
  document.body.innerHTML = ''
  closeSchedBoard()
  document.documentElement.dataset.motion = 'reduced'
  api.groupBoard.mockReset().mockResolvedValue(board())
  api.getGroupPolicy.mockReset().mockResolvedValue(policyFixture)
})
afterAll(() => { delete document.documentElement.dataset.motion })

describe('GroupSchedBoard', () => {
  it('renders the accounts in the order the API gives, never re-sorted', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' }, 'gpt-6.1-sol')
    await flushPromises()
    expect(api.groupBoard).toHaveBeenCalledWith(53, 'gpt-6.1-sol', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(rows().map((tr) => tr.dataset.sqBoardRow)).toEqual(['7', '3', '5', '9', '1'])
    expect(rows().map((tr) => tr.querySelector('td')!.textContent)).toEqual(['1', '2', '3', '—', '—'])
  })

  it('shows weak rows for hard admission with their reason, a countdown when it has an end, and the fallback word', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    const [first, cooling, demoted, limited, unknown] = rows()
    expect(limited.classList.contains('sq-weak')).toBe(true)
    expect(limited.classList.contains('sq-split')).toBe(true)
    expect(limited.querySelector('[data-sq-state]')!.textContent).toMatch(/^admin\.schedQuality\.reject\.rate_limit 2:(09|10)$/)
    // Numbers of weak rows are not coloured by verdict.
    expect(limited.querySelectorAll('[class*="sq-v-"]')).toHaveLength(0)
    expect(unknown.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.unknown')
    expect(unknown.classList.contains('sq-split')).toBe(false)

    expect(cooling.querySelector('[data-sq-state]')!.textContent).toMatch(/^admin\.schedQuality\.state\.cooling 1:(29|30)$/)
    expect(Array.from(cooling.querySelectorAll('td:nth-last-child(-n+3) span')).map((el) => el.className)).toEqual([
      'sq-focus sq-v-fail', 'sq-focus sq-v-pass', 'sq-focus sq-v-marginal'
    ])
    expect(demoted.querySelector('[data-sq-state]')!.textContent).toBe('admin.schedQuality.state.demoted')
    expect(demoted.querySelector('.sq-full')).not.toBeNull()
    // No-takeover mark: front, eligible, cannot take over, the group takes over, view live.
    expect(first.querySelector('[data-sq-no-takeover]')).not.toBeNull()
    expect(cooling.querySelector('[data-sq-no-takeover]')).toBeNull()
  })

  it('opens the account drawer on the board model from an account link', async () => {
    mount(GroupSchedBoard, { attachTo: document.body })
    openSchedBoard({ id: 53, name: '晨星组' })
    await flushPromises()
    document.querySelector<HTMLElement>('[data-sq-trigger="account:3"]')!.click()
    const { drawer } = useSchedLayers()
    expect([drawer.accountId, drawer.model]).toEqual([3, 'gpt-6.1-sol'])
  })
})
