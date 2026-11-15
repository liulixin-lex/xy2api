// Backend 0.6.5 lists administrator usage by request: unbilled (failed / canceled) requests sit between usage rows,
// rows carry a result and a switch count, and the stats split the requests by result.
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'
import UsageTable from '../UsageTable.vue'

vi.mock('@/utils/ipGeoLookup', () => ({ getEntry: vi.fn(() => ({ status: 'idle' })), fetchOne: vi.fn(), fetchBatch: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) })
}))

const baseStats = {
  total_requests: 12, total_input_tokens: 0, total_output_tokens: 0, total_cache_tokens: 0, total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0, total_tokens: 0, total_cost: 0, total_actual_cost: 0, total_account_cost: 0, average_duration_ms: 0
}

describe('UsageStatsCards — request outcomes', () => {
  it('splits the admin request count into success, rescued and failed', () => {
    const wrapper = mount(UsageStatsCards, { props: { stats: { ...baseStats, success: 9, rescued: 1, failed: 2 } }, global: { stubs: { Icon: true } } })
    expect(wrapper.get('[data-testid="usage-request-outcomes"]').text()).toBe('admin.usage.requestOutcomes:{"success":"9","rescued":"1","failed":"2"}')
    expect(wrapper.text()).not.toContain('usage.inSelectedRange')
  })

  it('keeps the range note for stats without the split (user page, older backend)', () => {
    const wrapper = mount(UsageStatsCards, { props: { stats: baseStats }, global: { stubs: { Icon: true } } })
    expect(wrapper.find('[data-testid="usage-request-outcomes"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('usage.inSelectedRange')
  })
})

describe('UsageTable — request rows', () => {
  const usageRow = {
    id: 5, source: 'usage', result: 2, switches: 2, request_id: 'r-usage', model: 'gpt-6.1-sol', account: { id: 1, name: 'acc-1' },
    input_tokens: 10, output_tokens: 5, cache_read_tokens: 0, cache_creation_tokens: 0, actual_cost: 0.01, total_cost: 0.01,
    rate_multiplier: 1, account_rate_multiplier: 1, billing_mode: 'token', image_count: 0
  }
  const unbilledRow = {
    id: 5, source: 'unbilled', result: 3, switches: 0, request_id: 'r-unbilled', model: 'gpt-6.1-sol', account: null,
    input_tokens: null, output_tokens: null, cache_read_tokens: null, cache_creation_tokens: null, actual_cost: null, total_cost: null,
    rate_multiplier: null, account_rate_multiplier: null, billing_mode: 'token', image_count: 0
  }
  const DataTableStub = {
    props: ['data', 'rowKey'],
    template: `<div><div v-for="row in data" :key="rowKey(row)" :data-key="rowKey(row)">
      <slot name="cell-account" :row="row" /><slot name="cell-tokens" :row="row" /><slot name="cell-cost" :row="row" />
    </div></div>`
  }
  const mountTable = () => mount(UsageTable, {
    props: { data: [usageRow, unbilledRow] as never, columns: [] },
    global: { stubs: { DataTable: DataTableStub, Icon: true, Teleport: true } }
  })

  it('keys rows by source and id, since usage and unbilled ids are separate sequences', () => {
    expect(mountTable().findAll('[data-key]').map((el) => el.attributes('data-key'))).toEqual(['usage:5', 'unbilled:5'])
  })

  it('marks a rescued request and its switch count under the account', () => {
    const [usage, unbilled] = mountTable().findAll('[data-key]')
    expect(usage.findAll('[data-testid="usage-request-marks"] > span').map((el) => el.text())).toEqual(['admin.usage.resultRescued', 'admin.usage.switches:{"n":2}'])
    expect(unbilled.find('[data-testid="usage-request-marks"]').exists()).toBe(false)
  })

  it('shows a dash instead of zero tokens and cost for a request without a bill, with its result word', () => {
    const [usage, unbilled] = mountTable().findAll('[data-key]')
    expect(unbilled.get('[data-testid="usage-unbilled"]').text()).toBe('—admin.usage.resultFailed')
    expect(unbilled.text()).not.toContain('$0.000000')
    expect(usage.find('[data-testid="usage-unbilled"]').exists()).toBe(false)
    expect(usage.text()).toContain('$0.010000')
  })
})
