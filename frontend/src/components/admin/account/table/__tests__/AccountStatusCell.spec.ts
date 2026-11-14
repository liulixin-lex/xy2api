import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick, ref } from 'vue'

import AccountStatusCell from '../AccountStatusCell.vue'

// The page clock, moved by hand; `t` counts how often the cell works out its words.
const { clock, t } = vi.hoisted(() => ({
  clock: { now: null as unknown as { value: number } },
  t: { fn: null as unknown as ReturnType<typeof import('vitest').vi.fn> }
}))
vi.mock('@/composables/useLiveClock', () => ({ useLiveClock: () => clock.now }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (...args: [string, Record<string, unknown>?]) => t.fn(...args) })
}))

const NOW = Date.parse('2026-11-14T12:00:00Z')
const later = (minutes: number) => new Date(NOW + minutes * 60_000).toISOString()
const account = (patch: Record<string, unknown> = {}) =>
  ({ id: 1, name: 'acc-1', status: 'active', schedulable: true, extra: {}, ...patch }) as never
const mountCell = (patch: Record<string, unknown> = {}) => mount(AccountStatusCell, { props: { account: account(patch) } })
const state = (patch: Record<string, unknown>) => mountCell(patch).get('[data-test="account-status"]').attributes('data-state')

beforeEach(() => {
  clock.now = ref(NOW)
  t.fn = vi.fn((key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key))
})
afterEach(() => {
  vi.restoreAllMocks()
})

// Status column (7d-12): one state, first match wins, and a countdown on the page clock.
describe('AccountStatusCell', () => {
  it('shows the first state that applies: error, inactive, limited, overloaded, temporary, quota, paused, normal', () => {
    const all = { rate_limit_reset_at: later(30), overload_until: later(20), temp_unschedulable_until: later(10), quota_limit: 10, quota_used: 10, schedulable: false }
    expect(state({ ...all, status: 'error' })).toBe('error')
    expect(state({ ...all, status: 'disabled' })).toBe('inactive')
    expect(state(all)).toBe('limited')
    expect(state({ ...all, rate_limit_reset_at: null })).toBe('overloaded')
    expect(state({ ...all, rate_limit_reset_at: null, overload_until: null })).toBe('temp')
    expect(state({ ...all, rate_limit_reset_at: null, overload_until: null, temp_unschedulable_until: null })).toBe('quota')
    expect(state({ schedulable: false })).toBe('paused')
    expect(state({})).toBe('ok')
    // An end time already past does not count.
    expect(state({ rate_limit_reset_at: later(-1) })).toBe('ok')
  })

  it('counts down on the page clock and ends the state when the time is up', async () => {
    const wrapper = mountCell({ rate_limit_reset_at: later(90) })
    expect(wrapper.get('.account-status__detail').text()).toContain('"time":"common.time.countdown.hoursMinutes:{\\"h\\":1,\\"m\\":30}"')
    clock.now.value = NOW + 60 * 60_000
    await nextTick()
    expect(wrapper.get('.account-status__detail').text()).toContain('common.time.countdown.minutes:{\\"m\\":30}')
    clock.now.value = NOW + 91 * 60_000
    await nextTick()
    expect(wrapper.get('[data-test="account-status"]').attributes('data-state')).toBe('ok')
  })

  it('does not work out a row without an end time again when the clock ticks (7d-5)', async () => {
    mountCell({})
    const calls = t.fn.mock.calls.length
    clock.now.value = NOW + 1000
    await nextTick()
    clock.now.value = NOW + 2000
    await nextTick()
    expect(t.fn.mock.calls.length).toBe(calls)
  })
})
